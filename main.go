package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	bolt "go.etcd.io/bbolt"
	dbus "github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/introspect"
)

var objPath = dbus.ObjectPath("/party/sammyette/Sparkline")
var busName = "party.sammyette.Sparkline"

type dataPoint struct{
	Percentage float64
	EnergyRate float64
}

type Sparkline struct{
	db *bolt.DB
	conn *dbus.Conn
	slconn *dbus.Conn
}

func (s *Sparkline) Collect(device string) (map[string]dataPoint, *dbus.Error) {
	data := make(map[string]dataPoint)
	err := s.db.View(func(tx *bolt.Tx) error {
		batB := tx.Bucket([]byte(device))
		if batB == nil {
			return fmt.Errorf("no such device %s", device)
		}

		// we dont really have to check for errors here, right?
		err := batB.ForEachBucket(func(bkName []byte) error {
			dp := dataPoint{}
			dataPointBucket := batB.Bucket(bkName)
			if dataPointBucket == nil {
				return fmt.Errorf("well that's weird")
			}

			err := dataPointBucket.ForEach(func(k, v []byte) error {
				switch string(k) {
					case "percent":
						percent, err := strconv.ParseFloat(string(v), 64)
						if err != nil {
							return fmt.Errorf("thats VERY weird.")
						}
						dp.Percentage = percent
					case "energyRate":
						energyRate, err := strconv.ParseFloat(string(v), 64)
						if err != nil {
							return fmt.Errorf("thats VERY weird.")
						}
						dp.EnergyRate = energyRate
				}

				return nil
			})
			if err != nil {
				return err
			}

			data[string(bkName)] = dp

			return nil
		})
		return err
	})

	var dbusErr *dbus.Error
	if err != nil {
		dbusErr = dbus.NewError(err.Error(), nil)
	}

	return data, dbusErr
}

func (s *Sparkline) Introspect() (string, *dbus.Error) {
	introspectData := &introspect.Node{
		Name: string(objPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{
				Name: busName,
				Signals: []introspect.Signal{
					{
						Name: "Update",
					},
				},
				Methods: []introspect.Method{
					{
						Name: "Collect",
						Args: []introspect.Arg{
							{
								Name: "device",
								Type: "s",
								Direction: "in",
							},
							{
								Name: "data",
								Type: "a(sdd)",
								Direction: "out",
							},
						},
					},
				},
			},
		},
	}
	return string(introspect.NewIntrospectable(introspectData)), nil
}

func (s *Sparkline) monitor(objectPath dbus.ObjectPath) {
	rule := fmt.Sprintf("type='signal',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged',path='%s'", objectPath)
	err := s.conn.BusObject().CallWithContext(context.Background(), "org.freedesktop.DBus.AddMatch", 0, rule).Store()
	if err != nil {
		panic(err)
	}

	device := s.conn.Object("org.freedesktop.UPower", objectPath)

	signals := make(chan *dbus.Signal, 10)
	s.conn.Signal(signals)

	for signal := range signals {
		if signal.Name == "org.freedesktop.DBus.Properties.PropertiesChanged" {
			changedProps := signal.Body[1].(map[string]dbus.Variant)
			/*
			interfaceName := signal.Body[0].(string)
			invalidatedProps := signal.Body[2].([]string)
			// Handle property changes
			fmt.Printf("Interface: %s\n", interfaceName)
			fmt.Println("Changed properties:")
			for propName, propValue := range changedProps {
				fmt.Printf("%s: %v\n", propName, propValue)
			}
			fmt.Println("Invalidated properties:", invalidatedProps)
			*/

			percent, _ := device.GetProperty("org.freedesktop.UPower.Device.Percentage")
			fmt.Println(percent.Value().(float64))

			var dp dataPoint

			err := s.db.Update(func(tx *bolt.Tx) error {
				percent := getProperty(device, "Percentage").(float64)
				energyRate := getProperty(device, "EnergyRate").(float64)

				batBucket := tx.Bucket([]byte(objectPath))
				b, err := batBucket.CreateBucket([]byte(strconv.FormatUint(changedProps["UpdateTime"].Value().(uint64), 10)))
				if err != nil {
					return err
				}

				b.Put([]byte("percent"), []byte(strconv.FormatFloat(percent, 'g', -1, 64)))
				b.Put([]byte("energyRate"), []byte(strconv.FormatFloat(energyRate, 'g', -1, 64)))

				dp = dataPoint{
					Percentage: percent,
					EnergyRate: energyRate,
				}

				return nil
			})
			if err != nil {
				panic(err)
			}

			s.slconn.Emit(objPath, busName + ".Update", dp)
		}
	}
}

func (s *Sparkline) export() {
	// Export your object
	err := s.slconn.Export(s, objPath, busName)
	if err != nil {
		panic(err)
	}

	err = s.slconn.Export(s, objPath, "org.freedesktop.DBus.Introspectable")
	if err != nil {
		panic(err)
	}

	// Request name on the bus
	_, err = s.slconn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		panic(err)
	}
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

	slconn, err := dbus.ConnectSessionBus()
	if err != nil {
		panic(err)
	}

	upower := conn.Object("org.freedesktop.UPower", "/org/freedesktop/UPower")
	ret := upower.Call("org.freedesktop.UPower.EnumerateDevices", 0)
	if ret.Err != nil {
		panic(err)
	}

	sl := Sparkline{
		db: db,
		conn: conn,
		slconn: slconn,
	}

	for _, path := range ret.Body[0].([]dbus.ObjectPath) {
		bat := conn.Object("org.freedesktop.UPower", path)
		powerSupply, _ := bat.GetProperty("org.freedesktop.UPower.Device.PowerSupply")
		present, _ := bat.GetProperty("org.freedesktop.UPower.Device.IsPresent")

		if powerSupply.Value().(bool) && present.Value().(bool){
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

			go sl.monitor(path)
		}
	}

	sl.export()

	done := make(chan struct{})
	<-done
}
