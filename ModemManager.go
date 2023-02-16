package modemmanager

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Paths of methods and properties
const (
	ModemManagerBusName        = "org.freedesktop.ModemManager1"
	ModemManagerInterface      = "org.freedesktop.ModemManager1"
	ModemManagerModemInterface = "org.freedesktop.ModemManager1.Modem"

	ModemManagerObjectPath     = "/org/freedesktop/ModemManager1"
	modemManagerMainObjectPath = "/org/freedesktop/ModemManager/"

	/* Methods */
	ModemManagerScanDevices       = ModemManagerInterface + ".ScanDevices"
	ModemManagerSetLogging        = ModemManagerInterface + ".SetLogging"
	ModemManagerReportKernelEvent = ModemManagerInterface + ".ReportKernelEvent"
	ModemManagerInhibitDevice     = ModemManagerInterface + ".InhibitDevice"

	/* Property */
	ModemManagerPropertyVersion = ModemManagerInterface + ".Version" // readable   s

)

type ModemChange struct {
	Change    MMModemChange
	ModemPath dbus.ObjectPath
}

// The ModemManager interface allows controlling and querying the status of the ModemManager daemon.
type ModemManager interface {
	/* METHODS */

	// Start a new scan for connected modem devices.
	ScanDevices() error

	// List modem devices. renamed from ListDevices to GetModems
	GetModems() ([]Modem, error)

	// Set logging verbosity.
	SetLogging(level MMLoggingLevel) error

	// Event Properties.
	// Reports a kernel event to ModemManager.
	// This method is only available if udev is not being used to report kernel events.
	// The properties dictionary is composed of key/value string pairs. The possible keys are:
	// see EventProperty and MMKernelPropertyAction
	ReportKernelEvent(EventProperties) error

	// org.freedesktop.ModemManager1.Modem:Device property. inhibit: TRUE to inhibit the modem and FALSE to uninhibit it.
	// Inhibit or uninhibit the device.
	// When the modem is inhibited ModemManager will close all its ports and unexport it from the bus, so that users of the interface are no longer able to operate with it.
	// This operation binds the inhibition request to the existence of the caller in the DBus bus. If the caller disappears from the bus, the inhibition will automatically removed.
	// 		IN s uid: the unique ID of the physical device, given in the
	// 		IN b inhibit:
	InhibitDevice(uid string, inhibit bool) error

	// The runtime version of the ModemManager daemon.
	GetVersion() (string, error)

	MarshalJSON() ([]byte, error)

	/* SIGNALS */

	SubscribeModemChanges() (<-chan *ModemChange, error)
	Unsubscribe()
}

// NewModemManager returns new ModemManager Interface
func NewModemManager() (ModemManager, error) {
	var mm modemManager
	return &mm, mm.init(ModemManagerInterface, ModemManagerObjectPath, &mm)
}

type modemManager struct {
	dbusBase
	modemChange      chan *ModemChange
	discoveredModems map[dbus.ObjectPath]bool
	sync.RWMutex
}

// EventProperties  defines the properties which should be reported to the kernel
type EventProperties struct {
	Action    MMKernelPropertyAction `json:"action"`    // The type of action, given as a string value (signature "s"). This parameter is MANDATORY.
	Name      string                 `json:"name"`      // The device name, given as a string value (signature "s"). This parameter is MANDATORY.
	Subsystem string                 `json:"subsystem"` // The device subsystem, given as a string value (signature "s"). This parameter is MANDATORY.
	Uid       string                 `json:"uid"`       // The unique ID of the physical device, given as a string value (signature "s"). This parameter is OPTIONAL, if not given the sysfs path of the physical device will be used. This parameter must be the same for all devices exposed by the same physical device.
}

// MarshalJSON returns a byte array
func (ep EventProperties) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"Action":    ep.Action,
		"Name ":     ep.Name,
		"Subsystem": ep.Subsystem,
		"Uid":       ep.Uid,
	})
}

func (mm *modemManager) GetModems() (modems []Modem, err error) {
	devPaths, err := mm.getManagedObjects(ModemManagerInterface, ModemManagerObjectPath)
	if err != nil {
		return nil, err
	}
	for idx := range devPaths {
		modem, err := NewModem(devPaths[idx])
		if err != nil {
			return nil, err
		}
		modems = append(modems, modem)
	}
	return
}

func (mm *modemManager) ScanDevices() error {
	err := mm.call(ModemManagerScanDevices)
	return err
}

func (mm *modemManager) SetLogging(level MMLoggingLevel) error {
	err := mm.call(ModemManagerSetLogging, &level)
	return err
}

func (mm *modemManager) ReportKernelEvent(properties EventProperties) error {
	// todo: untested
	v := reflect.ValueOf(properties)
	st := reflect.TypeOf(properties)
	type dynMap interface{}
	var myMap map[string]dynMap
	myMap = make(map[string]dynMap)
	for i := 0; i < v.NumField(); i++ {
		field := st.Field(i)
		tag := field.Tag.Get("json")
		value := v.Field(i).Interface()
		if v.Field(i).IsZero() {
			continue
		}
		myMap[tag] = value
	}
	return mm.call(ModemManagerReportKernelEvent, &myMap)
}

func (mm *modemManager) InhibitDevice(uid string, inhibit bool) error {
	// todo: untested
	err := mm.call(ModemManagerInhibitDevice, &uid, &inhibit)
	return err
}

func (mm *modemManager) GetVersion() (string, error) {
	v, err := mm.getStringProperty(ModemManagerPropertyVersion)
	return v, err
}

func (mm *modemManager) DeliverSignal(_, _ string, signal *dbus.Signal) {
	mm.Lock()
	defer mm.Unlock()

	// log.Printf("deliver signal bg %s", signal.Name)
	// log.Printf("%v", signal.Body)

	if mm.modemChange == nil {
		log.Printf("deliver signal ex0")
		return
	}

	if len(mm.modemChange) >= cap(mm.modemChange) {
		log.Printf("cannot handle signal since modem change channel is full")
		return
	}

	switch signal.Name {
	case dbusNameOwnerChanged:
		if len(signal.Body) != 3 {
			log.Printf("%s signal must have 3 args, got %d", signal.Name, len(signal.Body))
			return
		}

		// any time when ModemManager bus name changes we need to invalidate all discovered modems
		for path := range mm.discoveredModems {
			mm.modemChange <- &ModemChange{Change: MmModemChangeRemoved, ModemPath: path}
		}
		mm.discoveredModems = make(map[dbus.ObjectPath]bool)

	case dbusObjectManagerInterfacesAdded:
		if len(signal.Body) != 2 {
			log.Printf("%s signal must have 2 args, got %d", signal.Name, len(signal.Body))
			return
		}

		// log.Printf("handling %s for %s modem, props %v", signal.Name, signal.Body[0], signal.Body[1])
		path, ok := signal.Body[0].(dbus.ObjectPath)
		if !ok {
			log.Printf("%s signal first arg should be a dbus.ObjectPath, got %T", signal.Name, signal.Body[0])
			return
		}
		if !strings.HasPrefix(string(path), "/org/freedesktop/ModemManager1/Modem/") {
			return
		}

		mm.modemChange <- &ModemChange{Change: MmModemChangeAdded, ModemPath: path}
		mm.discoveredModems[path] = true

	case dbusObjectManagerInterfacesRemoved:
		if len(signal.Body) != 2 {
			log.Printf("%s signal must have 2 args, got %d", signal.Name, len(signal.Body))
			return
		}
		// log.Debugf("handling %s for %s modem, props %v", signal.Name, signal.Body[0], signal.Body[1])
		path, ok := signal.Body[0].(dbus.ObjectPath)
		if !ok {
			log.Printf("%s signal first arg should be a dbus.ObjectPath, got %T", signal.Name, signal.Body[0])
			return
		}

		mm.modemChange <- &ModemChange{Change: MmModemChangeRemoved, ModemPath: path}
		delete(mm.discoveredModems, path)
	}
}

func (mm *modemManager) SubscribeModemChanges() (<-chan *ModemChange, error) {
	mm.Lock()
	defer mm.Unlock()

	if mm.modemChange != nil {
		return mm.modemChange, nil
	}

	mm.modemChange = make(chan *ModemChange, 10)

	// filter modem additions and removals
	err := mm.conn.AddMatchSignal(
		dbus.WithMatchSender(ModemManagerBusName),
		dbus.WithMatchInterface(dbusObjectManagerInterface),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot set D-Bus match rule: %w", err)
	}

	// filter "org.freedesktop.ModemManager1" reconnection
	err = mm.conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface(dbusInterface),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, ModemManagerBusName),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot set D-Bus match rule: %w", err)
	}
	return mm.modemChange, nil
}

func (mm *modemManager) Unsubscribe() {
	mm.Lock()
	defer mm.Unlock()

	close(mm.modemChange)
	mm.modemChange = nil
}

func (mm *modemManager) MarshalJSON() ([]byte, error) {
	version, err := mm.GetVersion()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{
		"Version": version,
	})
}
