package main

import (
	"fmt"
	"log"

	"github.com/maltegrosse/go-modemmanager"
)

func main() {
	mmgr, err := modemmanager.NewModemManager()
	if err != nil {
		log.Fatal(err.Error())
	}
	version, err := mmgr.GetVersion()
	if err != nil {
		log.Fatal(err.Error())
	}
	fmt.Println("ModemManager Version: ", version)

	ch, err := mmgr.SubscribeModemChanges()
	if err != nil {
		log.Fatal(err.Error())
	}

	for c := range ch {
		fmt.Printf("change: %d, %v\n", c.Change, c.ModemPath)
		if c.Change == modemmanager.MmModemChangeAdded {
			modem, err := modemmanager.NewModem(c.ModemPath)
			if err != nil {
				log.Fatal(err.Error())
			}
			man, err := modem.GetManufacturer()
			if err != nil {
				log.Fatal(err.Error())
			}
			mod, err := modem.GetModel()
			if err != nil {
				log.Fatal(err.Error())
			}
			log.Printf("new modem is: %s %s\n", man, mod)

			// enable it
			// err = modem.Enable()
			// if err != nil {
			// 	log.Fatal(err.Error())
			// }
		} else {
			log.Printf("removed modem is: %s\n", c.ModemPath)
		}
	}
}
