package meshtastic

import (
	"go.bug.st/serial"
)

// OpenSerial connects to a USB-attached node (e.g. /dev/tty.usbserial-XXXX
// on macOS, /dev/ttyUSB0 or /dev/ttyACM0 on Linux). Meshtastic's serial
// console speaks the same stream protocol at 115200 baud.
func OpenSerial(device string) (*Radio, error) {
	port, err := serial.Open(device, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return nil, err
	}
	return Connect(port, Options{Serial: true})
}

// ListSerialPorts enumerates candidate serial devices (diagnostics/UI).
func ListSerialPorts() ([]string, error) {
	return serial.GetPortsList()
}
