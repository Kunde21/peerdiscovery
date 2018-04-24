package peerdiscovery

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

// Discovered is the structure of the discovered peers,
// which holds their local address (port removed) and
// a payload if there is one.
type Discovered struct {
	Address string
	Payload []byte
}

// Settings are the settings that can be specified for
// doing peer discovery.
type Settings struct {
	// Limit is the number of peers to discover, use < 1 for unlimited.
	Limit int
	// Port is the port to broadcast on (the peers must also broadcast using the same port).
	// The default port is 999.
	Port string
	// MulticastAddress specifies the multicast address.
	// You should be able to use any between 224.0.0.0 to 239.255.255.255.
	// By default it uses the Simple Service Discovery Protocol address (239.255.255.250).
	MulticastAddress string
	// Payload is the bytes that are sent out with each broadcast. Must be short.
	Payload []byte
	// Delay is the amount of time between broadcasts. The default delay is 1 second.
	Delay time.Duration
	// TimeLimit is the amount of time to spend discovering, if the limit is not reached.
	// The default time limit is 10 seconds.
	TimeLimit time.Duration

	portNum                 int
	multicastAddressNumbers []uint8
}

// PeerDiscovery is the object that can do the discovery for finding LAN peers.
type PeerDiscovery struct {
	settings Settings
	received map[string][]byte
	sync.RWMutex
}

// New returns a new PeerDiscovery object which can be used to discover peers.
// The settings are optional. If any setting is not supplied, then defaults are used.
// See the Settings for more information.
func New(settings ...Settings) (p *PeerDiscovery, err error) {
	p = new(PeerDiscovery)
	p.Lock()
	defer p.Unlock()
	if len(settings) > 0 {
		p.settings = settings[0]
	}
	// defaults
	if p.settings.Port == "" {
		p.settings.Port = "9999"
	}
	if p.settings.MulticastAddress == "" {
		p.settings.MulticastAddress = "239.255.255.250"
	}
	if len(p.settings.Payload) == 0 {
		p.settings.Payload = []byte("hi")
	}
	if p.settings.Delay == time.Duration(0) {
		p.settings.Delay = 1 * time.Second
	}
	if p.settings.TimeLimit == time.Duration(0) {
		p.settings.TimeLimit = 10 * time.Second
	}
	p.received = make(map[string][]byte)
	p.settings.multicastAddressNumbers = []uint8{0, 0, 0, 0}
	for i, num := range strings.Split(p.settings.MulticastAddress, ".") {
		var nInt int
		nInt, err = strconv.Atoi(num)
		if err != nil {
			return
		}
		p.settings.multicastAddressNumbers[i] = uint8(nInt)
	}
	p.settings.portNum, err = strconv.Atoi(p.settings.Port)
	if err != nil {
		return
	}
	return
}

// Discover will use the created settings to scan for LAN peers. It will return
// an array of the discovered peers and their associate payloads. It will not
// return broadcasts sent to itself.
func (p *PeerDiscovery) Discover() (discoveries []Discovered, err error) {
	p.RLock()
	address := p.settings.MulticastAddress + ":" + p.settings.Port
	portNum := p.settings.portNum
	multicastAddressNumbers := p.settings.multicastAddressNumbers
	payload := p.settings.Payload
	tickerDuration := p.settings.Delay
	timeLimit := p.settings.TimeLimit
	p.RUnlock()

	// get interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	// Open up a connection
	c, err := net.ListenPacket("udp4", address)
	if err != nil {
		return
	}
	defer c.Close()

	group := net.IPv4(multicastAddressNumbers[0], multicastAddressNumbers[1], multicastAddressNumbers[2], multicastAddressNumbers[3])
	p2 := ipv4.NewPacketConn(c)

	for i := range ifaces {
		if errJoinGroup := p2.JoinGroup(&ifaces[i], &net.UDPAddr{IP: group, Port: portNum}); errJoinGroup != nil {
			// log.Print(errJoinGroup)
			continue
		}
	}

	go p.listen()
	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()
	start := time.Now()
	for t := range ticker.C {
		exit := false
		p.Lock()
		if len(p.received) >= p.settings.Limit && p.settings.Limit > 0 {
			exit = true
		}
		p.Unlock()

		// write to multicast
		dst := &net.UDPAddr{IP: group, Port: portNum}
		for i := range ifaces {
			if errMulticast := p2.SetMulticastInterface(&ifaces[i]); errMulticast != nil {
				// log.Print(errMulticast)
				continue
			}
			p2.SetMulticastTTL(2)
			if _, errMulticast := p2.WriteTo([]byte(payload), nil, dst); errMulticast != nil {
				// log.Print(errMulticast)
				continue
			}
		}

		if exit || t.Sub(start) > timeLimit {
			break
		}
	}

	// send out broadcast that is finished
	dst := &net.UDPAddr{IP: group, Port: portNum}
	for i := range ifaces {
		if errMulticast := p2.SetMulticastInterface(&ifaces[i]); errMulticast != nil {
			continue
		}
		p2.SetMulticastTTL(2)
		if _, errMulticast := p2.WriteTo([]byte(payload), nil, dst); errMulticast != nil {
			continue
		}
	}

	p.Lock()
	discoveries = make([]Discovered, len(p.received))
	i := 0
	for ip := range p.received {
		discoveries[i] = Discovered{
			Address: ip,
			Payload: p.received[ip],
		}
		i++
	}
	p.Unlock()
	return
}

const (
	maxDatagramSize = 8192
)

// Listen binds to the UDP address and port given and writes packets received
// from that address to a buffer which is passed to a hander
func (p *PeerDiscovery) listen() (recievedBytes []byte, err error) {
	p.RLock()
	address := p.settings.MulticastAddress + ":" + p.settings.Port
	portNum := p.settings.portNum
	multicastAddressNumbers := p.settings.multicastAddressNumbers
	p.RUnlock()
	localIPs := getLocalIPs()

	// get interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	// log.Println(ifaces)

	// Open up a connection
	c, err := net.ListenPacket("udp4", address)
	if err != nil {
		return
	}
	defer c.Close()

	group := net.IPv4(multicastAddressNumbers[0], multicastAddressNumbers[1], multicastAddressNumbers[2], multicastAddressNumbers[3])
	p2 := ipv4.NewPacketConn(c)
	for i := range ifaces {
		if errJoinGroup := p2.JoinGroup(&ifaces[i], &net.UDPAddr{IP: group, Port: portNum}); errJoinGroup != nil {
			// log.Print(errJoinGroup)
			continue
		}
	}

	// Loop forever reading from the socket
	for {
		buffer := make([]byte, maxDatagramSize)
		n, _, src, errRead := p2.ReadFrom(buffer)
		// log.Println(n, src.String(), err, buffer[:n])
		if errRead != nil {
			err = errRead
			return
		}

		if _, ok := localIPs[strings.Split(src.String(), ":")[0]]; ok {
			continue
		}

		// log.Println(src, hex.Dump(buffer[:n]))

		p.Lock()
		if _, ok := p.received[strings.Split(src.String(), ":")[0]]; !ok {
			p.received[strings.Split(src.String(), ":")[0]] = buffer[:n]
		}
		if len(p.received) >= p.settings.Limit && p.settings.Limit > 0 {
			p.Unlock()
			break
		}
		p.Unlock()
	}

	return
}

// getLocalIPs returns the local ip address
func getLocalIPs() (ips map[string]struct{}) {
	ips = make(map[string]struct{})
	ips["localhost"] = struct{}{}
	ips["127.0.0.1"] = struct{}{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, address := range addrs {
		ips[strings.Split(address.String(), "/")[0]] = struct{}{}
	}
	return
}
