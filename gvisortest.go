package main

import (
	"fmt"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

var testMsg = "Hello, world!"

func setupStack(fd int, addr tcpip.Address) (*stack.Stack, error) {
	netStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol, tcp.NewProtocol},
		HandleLocal:        true,
	})
	endpoint, err := fdbased.New(&fdbased.Options{
		FDs:        []int{fd},
		MTU:        1500,
	})
	if err != nil {
		return nil, err
	}
	netStack.CreateNICWithOptions(1, endpoint, stack.NICOptions{
		Name:     "1",
	})
	netStack.AddProtocolAddress(1,
		tcpip.ProtocolAddress{
			Protocol: ipv6.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{
				Address:   addr,
				PrefixLen: 128,
			},
		},
		stack.AddressProperties{},
	)
	localNet := tcpip.AddressWithPrefix{
		Address:   tcpip.Address(net.ParseIP("FD00::0")),
		PrefixLen: 8,
	}
	netStack.AddRoute(tcpip.Route{
		Destination: localNet.Subnet(),
		NIC:         1,
	})
	return netStack, nil
}

func gonetListener(netStack *stack.Stack, port uint16) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		return gonet.ListenTCP(
			netStack,
			tcpip.FullAddress{
				NIC:  1,
				Port: port,
			},
			ipv6.ProtocolNumber)
	}
}

func netListener(addr net.IP, port int) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		return net.ListenTCP(
			"tcp6",
			&net.TCPAddr{
				IP:   addr,
				Port: port,
			},
		)
	}
}

func testServer(listenFunc func() (net.Listener, error)) {
	li, err := listenFunc()
	if err != nil {
		fmt.Printf("Listen error: %s\n", err)
		return
	}
	for {
		sc, err := li.Accept()
		if err != nil {
			fmt.Printf("accept error: %s\n", err)
			return
		}
		go func() {
			_, err = sc.Write([]byte(testMsg))
			if err != nil {
				fmt.Printf("write error: %s\n", err)
			}
			err = sc.Close()
			if err != nil {
				fmt.Printf("close error: %s\n", err)
			}
		}()
	}
}

func gonetDialer(netStack *stack.Stack, addr tcpip.Address, port uint16) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		return gonet.DialTCP(
			netStack,
			tcpip.FullAddress{
				NIC:  1,
				Addr: addr,
				Port: port,
			},
			ipv6.ProtocolNumber)
	}
}

func netDialer(addr net.IP, port int) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		return net.DialTCP(
			"tcp6",
			nil,
			&net.TCPAddr{
				IP:   addr,
				Port: port,
			},
		)
	}
}
func runTestConns(dialFunc func() (net.Conn, error), nConns int, wg *sync.WaitGroup) {
	for i := 0; i < nConns; i++ {
		go func() {
			defer wg.Done()
			c, err := dialFunc()
			if err != nil {
				fmt.Printf("dial TCP error: %s\n", err)
				return
			}
			b, err := io.ReadAll(c)
			if err != nil {
				fmt.Printf("read TCP error: %s\n", err)
				return
			}
			err = c.Close()
			if err != nil {
				fmt.Printf("close TCP error: %s\n", err)
				return
			}
			if string(b) != testMsg {
				fmt.Printf("incorrect data received: expected %s but got %s\n", testMsg, b)
				return
			}
		}()
	}
}

func runGonet(nConns int) error {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return err
	}
	addr1 := tcpip.Address(net.ParseIP("FD00::1"))
	stack1, err := setupStack(fds[0], addr1)
	if err != nil {
		return err
	}
	go testServer(gonetListener(stack1, 1234))
	addr2 := tcpip.Address(net.ParseIP("FD00::2"))
	stack2, err := setupStack(fds[1], addr2)
	if err != nil {
		return err
	}
	go testServer(gonetListener(stack2, 1234))
	time.Sleep(time.Millisecond)
	wg := &sync.WaitGroup{}
	wg.Add(nConns*2)
	go runTestConns(gonetDialer(stack1, addr2, 1234), nConns, wg)
	go runTestConns(gonetDialer(stack2, addr1, 1234), nConns, wg)
	wg.Wait()
	return nil
}

func runNet(nConns int) error {
	go testServer(netListener(net.ParseIP("::1"), 1234))
	go testServer(netListener(net.ParseIP("::1"), 4321))
	time.Sleep(time.Millisecond)
	wg := &sync.WaitGroup{}
	wg.Add(nConns*2)
	go runTestConns(netDialer(net.ParseIP("::1"), 1234), nConns, wg)
	go runTestConns(netDialer(net.ParseIP("::1"), 4321), nConns, wg)
	wg.Wait()
	return nil
}

func doRun(name string, runFunc func() error) {
	fmt.Printf("Starting %s\n", name)
	err := runFunc()
	if err != nil {
		fmt.Printf("Error: %s\n", err)
	}
	fmt.Printf("Finished %s\n", name)
}

func main() {
	doRun("runNet 100", func() error { return runNet(100) })
	doRun("runGonet 10", func() error { return runGonet(10) })
	doRun("runGonet 100", func() error { return runGonet(100) })
}
