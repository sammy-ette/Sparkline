package main

import (
	"os"

	dbus "github.com/godbus/dbus/v5"
)

func getProperty(obj dbus.BusObject, property string) interface{} {
	val, err := obj.GetProperty("org.freedesktop.UPower.Device." + property)
	if err != nil {
		panic("failed to retrieve property from battery object" + property)
	}

	return val.Value()
}

func getenv(env, fallback string) string {
	envValue := os.Getenv(env)
	if envValue == "" {
		return fallback
	}

	return envValue
}
