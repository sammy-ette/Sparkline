package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	bolt "go.etcd.io/bbolt"
	dbus "github.com/godbus/dbus/v5"
)

func getenv(env, fallback string) string {
	envValue := os.Getenv(env)
	if envValue == "" {
		return fallback
	}

	return envValue
}

func main() {
	home, _ := os.UserHomeDir()
	localShare := filepath.Join(home, ".local", "share")
	databasePath := filepath.Join(getenv("XDG_DATA_HOME", localShare), "sparkline", "sparkline.db")

	os.MkdirAll(filepath.Dir(databasePath), 0755)

	db, err := bolt.Open(databasePath, 0755, nil)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	upower := conn.Object("org.freedesktop.UPower", "/org/freedesktop/UPower")
	ret := upower.Call("org.freedesktop.UPower.EnumerateDevices", 0)
	if ret.Err != nil {
		panic(err)
	}

	var batteryPath dbus.ObjectPath
	var battery dbus.BusObject
	for _, path := range ret.Body[0].([]dbus.ObjectPath) {
		bat := conn.Object("org.freedesktop.UPower", path)
		powerSupply, _ := bat.GetProperty("org.freedesktop.UPower.Device.PowerSupply")
		present, _ := bat.GetProperty("org.freedesktop.UPower.Device.IsPresent")

		if powerSupply.Value().(bool) && present.Value().(bool){
			battery = bat
			batteryPath = path

			err := db.Update(func(tx *bolt.Tx) error {
				_, err := tx.CreateBucketIfNotExists([]byte(path))
				if err != nil {
					return fmt.Errorf(":( database bucket creation error: %s", err)
				}

				return nil
			})

			if err != nil {
				panic(err)
			}
		}
	}

	// Add match rule for property change signals
	rule := fmt.Sprintf("type='signal',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged',path='%s'", batteryPath)
	err = conn.BusObject().CallWithContext(context.Background(), "org.freedesktop.DBus.AddMatch", 0, rule).Store()
	if err != nil {
		panic(err)
	}

	// Handle property change signals
	signals := make(chan *dbus.Signal, 10)
	conn.Signal(signals)

	for signal := range signals {
		if signal.Name == "org.freedesktop.DBus.Properties.PropertiesChanged" {
			interfaceName := signal.Body[0].(string)
			changedProps := signal.Body[1].(map[string]dbus.Variant)
			invalidatedProps := signal.Body[2].([]string)
			// Handle property changes
			fmt.Printf("Interface: %s\n", interfaceName)
			fmt.Println("Changed properties:")
			for propName, propValue := range changedProps {
				fmt.Printf("%s: %v\n", propName, propValue)
			}
			fmt.Println("Invalidated properties:", invalidatedProps)

			percent, _ := battery.GetProperty("org.freedesktop.UPower.Device.Percentage")
			fmt.Println(percent.Value().(float64))

			err := db.Update(func(tx *bolt.Tx) error {
				percent := getProperty(battery, "Percentage").(float64)
				energyRate := getProperty(battery, "EnergyRate").(float64)

				batBucket := tx.Bucket([]byte(batteryPath))
				b, err := batBucket.CreateBucket([]byte(strconv.FormatUint(changedProps["UpdateTime"].Value().(uint64), 10)))
				if err != nil {
					return err
				}

				b.Put([]byte("percent"), []byte(strconv.FormatFloat(percent, 'g', -1, 64)))
				b.Put([]byte("energyRate"), []byte(strconv.FormatFloat(energyRate, 'g', -1, 64)))

				return nil
			})

			if err != nil {
				panic(err)
			}
		}
	}
}

func getProperty(obj dbus.BusObject, property string) interface{} {
	val, err := obj.GetProperty("org.freedesktop.UPower.Device." + property)
	if err != nil {
		panic("failed to retrieve property from battery object" + property)
	}

	return val.Value()
}
