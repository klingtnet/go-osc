/*
 * go-osc provides a package for sending and receiving OpenSoundControl messages.
 * The package is implemented in pure Go.
 *
 * The implementation is based on the Open Sound Control 1.0 Specification
 * (http://opensoundcontrol.org/spec-1_0).
 *
 * Open Sound Control (OSC) is an open, transport-independent, message-based
 * protocol developed for communication among computers, sound synthesizers,
 * and other multimedia devices.
 *
 * This OSC implementation uses the UDP protocol for sending and receiving
 * OSC packets.
 *
 * The unit of transmission of OSC is an OSC Packet. Any application that sends
 * OSC Packets is an OSC Client; any application that receives OSC Packets is
 * an OSC Server.
 *
 * An OSC packet consists of its contents, a contiguous block of binary data,
 * and its size, the number of 8-bit bytes that comprise the contents. The
 * size of an OSC packet is always a multiple of 4.
 *
 * OSC packets come in two flavors:
 *
 * OSC Messages: An OSC message consists of an OSC address pattern, followed
 * by an OSC Type Tag String, and finally by zero or more OSC arguments.
 *
 * OSC Bundles: An OSC Bundle consists of the string "#bundle" followed
 * by an OSC Time Tag, followed by zero or more OSC bundle elements. Each bundle
 * element can be another OSC bundle (note this recursive definition: bundle may
 * contain bundles) or OSC message.
 *
 * An OSC bundle element consists of its size and its contents. The size is
 * an int32 representing the number of 8-bit bytes in the contents, and will
 * always be a multiple of 4. The contents are either an OSC Message or an
 * OSC Bundle.
 *
 * The following argument types are supported: 'i' (Int32), 'f' (Float32),
 * 's' (string), 'b' (blob / binary data), 'h' (Int64), 't' (OSC timetag),
 * 'd' (Double), 'r' (RGBA color), 'T' (True), 'F' (False), 'N' (Nil).
 *
 * Supported OSC address patterns: TODO
 *
 * TODO: OscClient and OscServer
 *
 * Example usage:
 *
 * TODO
 *
 * Author: Sebastian Ruml <sebastian.ruml@gmail.com>
 * Created: 2013.08.19
 *
 */

package osc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	// The time tag value consisting of 63 zero bits followed by a one in the
	// least signifigant bit is a special case meaning "immediately."
	timeTagImmediate      = uint64(1)
	secondsFrom1900To1970 = 2208988800
)

// OscPacket is the interface for OscMessage and OscBundle.
type OscPacket interface {
	ToByteArray() (buffer []byte, err error)
}

// Represents a single OSC message. An OSC message consists of an OSC address
// pattern and zero or more arguments.
type OscMessage struct {
	Address   string
	arguments []interface{}
}

// An OSC Bundle consists of the OSC-string "#bundle" followed by an OSC Time Tag,
// followed by zero or more OSC bundle/message elements. The OSC-timetag is a 64-bit fixed
// point time tag. See http://opensoundcontrol.org/spec-1_0 for more information.
type OscBundle struct {
	Timetag  OscTimetag
	Messages []*OscMessage
	Bundles  []*OscBundle
}

// An OSC client. It sends OSC messages and bundles to the given IP address
// and port.
type OscClient struct {
	ipaddress string
	port      int
}

// An OSC server. The server listens on Address and Port for incoming OSC packets
// and bundles.
type OscServer struct {
	Address     string        // Address to listen on
	Port        int           // Port to listen on
	ReadTimeout time.Duration // Read Timeout
	dispatcher  OscDispatcher // Dispatcher that dispatches OSC packets/messages
	running     string
}

// OscTimetag represents an OSC Time Tag.
// An OSC Time Tag is defined as follows:
// Time tags are represented by a 64 bit fixed point number. The first 32 bits
// specify the number of seconds since midnight on January 1, 1900, and the
// last 32 bits specify fractional parts of a second to a precision of about
// 200 picoseconds. This is the representation used by Internet NTP timestamps.
type OscTimetag struct {
	timeTag  uint64 // The acutal time tag
	time     time.Time
	MinValue uint64 // Minimum value of an OSC Time Tag. Is always 1.
}

// Interface for an OSC message dispatcher. A dispatcher is responsible for
// dispatching received OSC messages.
type Dispatcher interface {
	Dispatch(bundle *OscBundle)
}

// OSC message handler interface. Every handler function for an OSC message must
// implement this interface.
type Handler interface {
	HandleMessage(bundle *OscBundle)
}

// Type defintion for an OSC handler function
type HandlerFunc func(bundle *OscBundle)

// HandleMessage calls themeself with the given OSC Message. Implements the
// Handler interface.
func (f HandlerFunc) HandleMessage(bundle *OscBundle) {
	f(bundle)
}

////
// OscDispatcher
////

// Dispatcher for OSC packets.
type OscDispatcher struct {
	handlers map[string]Handler
}

// NewOscDispatcher returns an OscDispatcher.
func NewOscDispatcher() (dispatcher *OscDispatcher) {
	return &OscDispatcher{handlers: make(map[string]Handler)}
}

// AddMsgHandler adds a new message handler for the given OSC address.
func (self *OscDispatcher) AddMsgHandler(address string, handler HandlerFunc) {
	self.handlers[address] = handler
}

// AddMsgHandlerFunc adds a new message handler for the given OSC address and
// handler function.
func (self *OscDispatcher) AddMsgHandlerFunc(address string, handler func(msg OscPacket)) {
	self.AddMsgHandler(address, HandlerFunc(handler))
}

// Dispatch dispatches OSC packets. Implements the Dispatcher interface.
// TODO: Rework this method.
func (self *OscDispatcher) Dispatch(bundle *OscBundle) {
	switch t := bundle.(type) {
	default:
		return

	case *OscMessage:
		msg, _ := bundle.(*OscMessage)
		for address, handler := range self.handlers {
			if msg.Match(address) {
				handler.HandleMessage(msg)
			}
		}

	case *OscBundle:
		// TODO: Wait with the dispatching until the time of the time tag is reached
		bundle, _ := bundle.(*OscBundle)
		for _, message := range bundle.messages {
			for address, handler := range self.handlers {
				if message.Match(address) {
					handler.HandleMessage(message)
				}
			}
		}

		// Process bundles
		for _, b := range bundle.bundles {
			self.Dispatch(b)
		}
	}
}

////
// OscMessage
////

// NewMessage returns a new OscMessage. The address parameter is the OSC address.
func NewOscMessage(address string) (msg *OscMessage) {
	return &OscMessage{Address: address}
}

// Arguments returns all arguments.
func (msg *OscMessage) Arguments() []interface{} {
	return msg.arguments
}

// Append appends the given argument to the arguments list.
func (msg *OscMessage) Append(argument interface{}) (err error) {
	if argument == nil {
		return err
	}

	msg.arguments = append(msg.arguments, argument)

	return nil
}

// Equals determines if the given OSC Message b is equal to the current OSC Message.
// It checks if the OSC address and the arguments are equal. Returns, true if the
// current object and b are equal.
func (msg *OscMessage) Equals(b *OscMessage) bool {
	// Check OSC address
	if msg.Address != b.Address {
		return false
	}

	// Check if the number of arguments are equal
	if msg.CountArguments() != b.CountArguments() {
		return false
	}

	// Check arguments
	for i, arg := range msg.arguments {
		if b.arguments[i] != arg {
			return false
		}
	}

	return true
}

// Clear clears the OSC address and all arguments.
func (msg *OscMessage) Clear() {
	msg.Address = ""
	msg.ClearData()
}

// ClearData removes all arguments from the OSC Message.
func (msg *OscMessage) ClearData() {
	msg.arguments = msg.arguments[len(msg.arguments):]
}

// Returns true, if the address of the OSC Message matches the given address.
// Case sensitive!
func (msg *OscMessage) Match(address string) bool {
	// TODO: Implement the pattern matching!

	if msg.Address == address {
		return true
	}

	return true
}

// CountArguments returns the number of arguments.
func (msg *OscMessage) CountArguments() int {
	return len(msg.arguments)
}

// ToByteBuffer serializes the OSC message to a byte buffer. The byte buffer
// is of the following format:
// 1. OSC Address Pattern
// 2. OSC Type Tag String
// 3. OSC Arguments
func (msg *OscMessage) ToByteArray() (buffer []byte, err error) {
	// The byte buffer for the message
	var data = new(bytes.Buffer)

	// We can start with the OSC address and add it to the buffer
	_, err = writePaddedString(msg.Address, data)
	if err != nil {
		return nil, err
	}

	// Type tag string starts with ","
	typetags := []byte{','}

	// Process the type tags and collect all arguments
	var payload = new(bytes.Buffer)
	for _, arg := range msg.arguments {
		// FIXME: Use t instead of arg
		switch t := arg.(type) {
		default:
			return nil, errors.New(fmt.Sprintf("OSC - unsupported type: %T", t))

		case bool:
			if arg.(bool) == true {
				typetags = append(typetags, 'T')
			} else {
				typetags = append(typetags, 'F')
			}

		case nil:
			typetags = append(typetags, 'N')

		case int32:
			typetags = append(typetags, 'i')

			if err = binary.Write(payload, binary.BigEndian, int32(t)); err != nil {
				return nil, err
			}

		case float32:
			typetags = append(typetags, 'f')

			if err = binary.Write(payload, binary.BigEndian, float32(t)); err != nil {
				return nil, err
			}

		case string:
			typetags = append(typetags, 's')

			if _, err = writePaddedString(t, payload); err != nil {
				return nil, err
			}

		case []byte:
			typetags = append(typetags, 'b')

			if _, err = writeBlob(t, payload); err != nil {
				return nil, err
			}

		case int64:
			typetags = append(typetags, 'h')

			if err = binary.Write(payload, binary.BigEndian, int64(t)); err != nil {
				return nil, err
			}

		case float64:
			typetags = append(typetags, 'd')

			if err = binary.Write(payload, binary.BigEndian, float64(t)); err != nil {
				return nil, err
			}

		case OscTimetag:
			typetags = append(typetags, 't')

			timeTag := arg.(OscTimetag)
			payload.Write(timeTag.ToByteArray())
		}
	}

	// Write the type tag string to the data buffer
	_, err = writePaddedString(string(typetags), data)
	if err != nil {
		return nil, err
	}

	// Write the payload (OSC arguments) to the data buffer
	data.Write(payload.Bytes())

	return data.Bytes(), nil
}

////
// OscBundle
////

// NewOscBundle returns an OSC Bundle. Use this function to create a new OSC
// Bundle.
func NewOscBundle(time time.Time) (bundle *OscBundle) {
	return &OscBundle{Timetag: *NewOscTimetag(time)}
}

// Append appends an OSC packet (OSC bundle or message) to the bundle.
func (self *OscBundle) Append(pck OscPacket) (err error) {
	switch t := pck.(type) {
	default:
		return errors.New(fmt.Sprintf("Unsupported OSC packet type: only OscBundle and OscMessage are supported.", t))

	case *OscBundle:
		self.bundles = append(self.bundles, t)

	case *OscMessage:
		self.messages = append(self.messages, t)
	}

	return nil
}

// ToByteArray serializes the OSC bundle to a byte array with the following format:
// 1. Bundle string: '#bundle'
// 2. OSC timetag
// 3. Length of first OSC bundle element
// 4. First bundle element
// 5. Length of x OSC bundle element
// 6. x bundle element
func (self *OscBundle) ToByteArray() (buffer []byte, err error) {
	var data = new(bytes.Buffer)

	// Add the '#bundle' string
	_, err = writePaddedString("#bundle", data)
	if err != nil {
		return nil, err
	}

	// Add the timetag
	if _, err = data.Write(self.Timetag.ToByteArray()); err != nil {
		return nil, err
	}

	// Process all OSC Messages
	for _, m := range self.Messages {
		var msgLen int
		var msgBuf []byte

		msgBuf, err = m.ToByteArray()
		if err != nil {
			return nil, err
		}

		// Append the length of the OSC message
		msgLen = len(msgBuf)
		if err = binary.Write(data, binary.BigEndian, int32(msgLen)); err != nil {
			return nil, err
		}

		// Append the OSC message
		data.Write(msgBuf)
	}

	// Process all OSC Bundles
	for _, b := range self.Bundles {
		var bLen int
		var bBuf []byte

		bBuf, err = b.ToByteArray()
		if err != nil {
			return nil, err
		}

		// Write the size of the bundle
		bLen = len(bBuf)
		if err = binary.Write(data, binary.BigEndian, int32(bLen)); err != nil {
			return nil, err
		}

		// Append the bundle
		data.Write(bBuf)
	}

	return data.Bytes(), nil
}

////
// OscClient
////

// NewOscClient creates a new OSC client. The OscClient is used to send OSC
// messages and OSC bundles over an UDP network connection. The argument ip
// specifies the IP address and port defines the target port where the messages
// and bundles will be send to.
func NewOscClient(ip string, port int) (client *OscClient) {
	return &OscClient{ipaddress: ip, port: port}
}

// Ip returns the IP address.
func (client *OscClient) Ip() string {
	return client.ipaddress
}

// SetIp sets a new IP address.
func (client *OscClient) SetIp(ip string) {
	client.ipaddress = ip
}

// Port returns the port.
func (client *OscClient) Port() int {
	return client.port
}

// SetPort sets a new port.
func (client *OscClient) SetPort(port int) {
	client.port = port
}

// Send sends an OSC Bundle or an OSC Message.
func (client *OscClient) Send(packet OscPacket) (err error) {
	addr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", client.ipaddress, client.port))
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}

	data, err := packet.ToByteArray()
	if err != nil {
		conn.Close()
		return err
	}

	_, err = conn.Write(data)
	if err != nil {
		conn.Close()
		return err
	}

	conn.Close()

	return nil
}

////
// OscServer
////

// NewOscServer returns a new OscServer.
func NewOscServer(address string, port int) (server *OscServer) {
	return &OscServer{
		Address:     address,
		Port:        port,
		dispatcher:  NewOscDispatcher(),
		ReadTimeout: 0,
		running:     true}
}

// ListenAndServe retrieves incoming OSC packets.
// TODO: Add support for server running in a goroutine
func (self *OscServer) ListenAndServe() error {
	if self.dispatcher == nil {
		return errors.New("No dispatcher definied")
	}

	service := fmt.Sprintf("%s:%d", self.Address, self.Port)
	udpAddr, err := net.ResolveUDPAddr("udp", service)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	// Set read timeout
	if self.ReadTimeout != 0 {
		conn.SetReadDeadline(time.Now().Add(self.ReadTimeout))
	}

	self.running = true
	var msg *OscBundle
	for {
		msg, err := self.readFromConnection(conn)
		if err == nil {
			// TODO: Every dispatch should happen in a new goroutine
			self.dispatcher.Dispatch(msg)
		}
	}

	panic("Unreachable - This should never happen.")
}

func (self *OscServer) Close() {
	self.running = false
}

func (self *OscServer) AddMsgHandler(address string, handler HandlerFunc) {
	self.dispatcher.AddMsgHandler(address, handler)
}

func (self *OscServer) AddMsgHandlerFunc(address string, handler func(pck OscPacket)) {
	self.dispatcher.AddMsgHandlerFunc(address, handler)
}

// readFromConnection retrieves OSC packets from the given io.Reader. If an OSC
// message is received an OSC Bundle will created and the message is appended to the
// bundle.
func (self *OscServer) readFromConnection(conn *net.UDPConn) (bundle *OscBundle, err error) {
	// func (self *OscServer) readFromConnection(reader io.Reader) (bundle *OscBundle, err error) {
	buf := make([]byte, 1024)

	// Read the next UDP packet
	n, _, err = conn.ReadFromUDP(b)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(bytes.Buffer(buf))

	// Read the first byte from the reader. Otherwise, wait until some data is received.
	buf, err = reader.Peek(1)
	if err != nil {
		return nil, err
	}

	// An OSC Message starts with a '/'
	if buf[0] == '/' {
		// Let's assume that the bundle starts immediate
		bundle = NewOscBundle(time.Now())

		var msg *OscMessage
		msg, err = self.readMessage(reader)
		if err != nil {
			return nil, err
		}

		bundle.Append(msg)
	} else if buf[0] == '#' { // An OSC bundle starts with a '#'
		bundle, err = self.readBundle(reader)
		if err != nil {
			return nil, err
		}
	}

	return bundle, nil
}

// readBundle reads a OscBundle from reader.
func (self *OscServer) readBundle(reader *bufio.Reader) (bundle *OscBundle, err error) {
	// Read the '#bundle' OSC string
	var startTag string
	startTag, err = readPaddedString(reader)
	if err != nil {
		return nil, err
	}

	if startTag != "#bundle" {
		return nil, errors.New(fmt.Sprintf("Invalid bundle start tag: %s", startTag))
	}

	// Read the timetag
	var timeTag int64
	if err := binary.Read(reader, binary.BigEndian, &timeTag); err != nil {
		return nil, err
	}

	// Create a new bundle
	bundle = NewOscBundle(timetagToTime(timeTag))

	// Read the size of the first bundle element
	var msgLen Int32
	msgLen = binary.Read(reader, binary.BigEndian, &msgLen)
	if msgLen < 1 {
		return nil, errors.New("No bundle element found")
	}

	var buf []byte
	buf, err = reader.Peek(1)
	if err != nil {
		return nil, err
	}

	// An OSC message starts with '/'
	if buf[0] == '/' {
		var msg *OscMessage
		msg, err = self.readMessage(reader)
		if err != nil {
			return nil, err
		}

		bundle.Append(msg)
	} else if buf[0] == '#' { // An OSC bundle starts with '#'
		// Recursivly unpack all bundles
		b, err = self.readBundle(reader)
		if err != nil {
			return nil, err
		}

		bundle.Append(b)
	}

	return nil, bundle
}

// readMessage reads one OSC Message from reader.
func (self *OscServer) readMessage(reader *bufio.Reader) (msg *OscMessage, err error) {
	// First, read the OSC address
	address, err := readPaddedString(reader)
	if err != nil {
		return nil, err
	}

	// Create a new message
	msg = NewOscMessage(address)

	// Read all arguments
	if err = self.readArguments(msg, reader); err != nil {
		return nil, err
	}

	return msg, nil
}

// readArguments reads all arguments from the reader and adds it to the OSC message.
func (self *OscServer) readArguments(msg *OscMessage, reader *bufio.Reader) error {
	// Read the type tag string
	typetags, err := readPaddedString(reader)
	if err != nil {
		return err
	}

	// If the typetag doesn't start with ',', it's not valid
	if typetags[0] != ',' {
		return errors.New("Unsupported type tag string")
	}

	// Remove ',' from the type tag
	typetags = typetags[1:]

	for _, c := range typetags {
		switch c {
		default:
			return errors.New(fmt.Sprintf("Unsupported type tag: %c", c))

		// int32
		case 'i':
			var i int32
			if err = binary.Read(reader, binary.BigEndian, &i); err != nil {
				return err
			}
			msg.Append(i)

		// int64
		case 'h':
			var i int64
			if err = binary.Read(reader, binary.BigEndian, &i); err != nil {
				return err
			}
			msg.Append(i)

		// float32
		case 'f':
			var f float32
			if err = binary.Read(reader, binary.BigEndian, &f); err != nil {
				return err
			}
			msg.Append(f)

		// float64/double
		case 'd':
			var d float64
			if err = binary.Read(reader, binary.BigEndian, &d); err != nil {
				return err
			}
			msg.Append(d)

		// string
		case 's':
			var s string
			if s, err = readPaddedString(reader); err != nil {
				return err
			}
			msg.Append(s)

		// blob
		case 'b':
			var buf []byte
			if buf, err = readBlob(reader); err != nil {
				return err
			}
			msg.Append(buf)

		// OSC Time Tag
		case 't':
			var tt uint64
			if err = binary.Read(reader, binary.BigEndian, &tt); err != nil {
				return nil
			}
			msg.Append(NewOscTimetagFromTimetag(tt))

		// True
		case 'T':
			var t bool
			if err = binary.Read(reader, binary.BigEndian, &t); err != nil {
				return err
			}
			msg.Append(t)

		// False
		case 'F':
			var t bool
			if err = binary.Read(reader, binary.BigEndian, &t); err != nil {
				return err
			}
			msg.Append(t)
		}
	}

	return nil
}

////
// OscTimetag
////

// NewOscTimetag returns a new OSC timetag object.
func NewOscTimetag(timeStamp time.Time) (timetag *OscTimetag) {
	return &OscTimetag{
		time:     timeStamp,
		timeTag:  timeToTimetag(timeStamp),
		MinValue: uint64(1)}
}

// NewOscTimetagFromTimetag creates a new OscTimetag from the given time tag.
func NewOscTimetagFromTimetag(timetag uint64) (t *OscTimetag) {
	time := timetagToTime(timetag)
	return NewOscTimetag(time)
}

// Time returns the time.
func (self *OscTimetag) Time() time.Time {
	return self.time
}

// FractionalSecond returns the last 32 bits of the Osc Time Tag. Specifies the
// fractional part of a second.
func (self *OscTimetag) FractionalSecond() uint32 {
	return uint32(self.timeTag << 32)
}

// SecondsSinceEpoch returns the first 32 bits (the number of seconds since the
// midnight 1900) from the OSC timetag.
func (self *OscTimetag) SecondsSinceEpoch() uint32 {
	return uint32(self.timeTag >> 32)
}

// TimeTag returns the time tag value
func (self *OscTimetag) TimeTag() uint64 {
	return self.timeTag
}

// ToByteArray converts the OSC Time Tag to a byte array.
func (self *OscTimetag) ToByteArray() []byte {
	var data = new(bytes.Buffer)
	binary.Write(data, binary.BigEndian, self.timeTag)
	return data.Bytes()
}

// SetTime sets the value of the OSC Time Tag.
func (self *OscTimetag) SetTime(time time.Time) {
	self.time = time
	self.timeTag = timeToTimetag(time)
}

////
// Decoding functions
////

// readBlob reads an OSC Blob from the blob byte array. Padding bytes are removed
// from the reader and not returned.
func readBlob(reader *bufio.Reader) (blob []byte, err error) {
	var blobData = new(bytes.Buffer)

	// TODO

	// First, get the length

	// Read the data

	// Remove the padding bytes

	return blobData.Bytes(), nil
}

// writeBlob writes the data byte array as an OSC blob into buff. If the length of
// data isn't 32-bit aligned, padding bytes will be added.
func writeBlob(data []byte, buff *bytes.Buffer) (numberOfBytes int, err error) {
	// Add the size of the blob
	dlen := int32(len(data))
	err = binary.Write(buff, binary.BigEndian, dlen)
	if err != nil {
		return 0, err
	}

	// Write the data
	if _, err = buff.Write(data); err != nil {
		return 0, nil
	}

	// Add padding bytes if necessary
	numPadBytes := padBytesNeeded(len(data))
	if numPadBytes > 0 {
		padBytes := make([]byte, numPadBytes)
		if numPadBytes, err = buff.Write(padBytes); err != nil {
			return 0, err
		}
	}

	return 4 + len(data) + numPadBytes, nil
}

// readPaddedString reads a padded string from the given reader. The padding bytes
// are removed from the reader.
func readPaddedString(reader *bufio.Reader) (str string, err error) {
	// Read the string from the reader
	str, err = reader.ReadString(0)
	if err != nil {
		return "", err
	}

	// Remove the delimiter
	str = str[:len(str)-1]

	// Remove the padding bytes
	padLen := padBytesNeeded(len(str)) - 1
	if padLen > 0 {
		padBytes := make([]byte, padLen)
		if _, err = reader.Read(padBytes); err != nil {
			return "", err
		}
	}

	return str, nil
}

// writePaddedString writes a string with padding bytes to the a buffer.
// Returns, the number of written bytes and an error if any.
func writePaddedString(str string, buff *bytes.Buffer) (numberOfBytes int, err error) {
	// Write the string to the buffer
	n, err := buff.WriteString(str)
	if err != nil {
		return 0, err
	}

	// Calculate the padding bytes needed and create a buffer for the padding bytes
	numPadBytes := padBytesNeeded(len(str))
	if numPadBytes > 0 {
		padBytes := make([]byte, numPadBytes)
		// Add the padding bytes to the buffer
		if numPadBytes, err = buff.Write(padBytes); err != nil {
			return 0, err
		}
	}

	return n + numPadBytes, nil
}

// padBytesNeeded determines how many bytes are needed to fill up to the next 4
// byte length.
func padBytesNeeded(elementLen int) int {
	return 4*(elementLen/4+1) - elementLen
}

////
// Timetag utility functions
////

// timeToTimetag converts the given time to an OSC timetag.
//
// An OSC timetage is defined as follows:
// Time tags are represented by a 64 bit fixed point number. The first 32 bits
// specify the number of seconds since midnight on January 1, 1900, and the
// last 32 bits specify fractional parts of a second to a precision of about
// 200 picoseconds. This is the representation used by Internet NTP timestamps.
//
// The time tag value consisting of 63 zero bits followed by a one in the least
// signifigant bit is a special case meaning "immediately."
func timeToTimetag(time time.Time) (timetag uint64) {
	timetag = uint64((secondsFrom1900To1970 + time.Unix()) << 32)
	return (timetag + uint64(uint32(time.Nanosecond())))
}

// timetagToTime converts the given timetag to a time object.
func timetagToTime(timetag uint64) (t time.Time) {
	return time.Unix(int64((timetag>>32)-secondsFrom1900To1970), int64(timetag&0xffffffff))
}

////
// Functions for pretty printing an OSC packet
////

func PrintOscPacket(writer io.Writer, pck OscPacket) {
	// TODO
}
