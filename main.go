package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
	"github.com/jnovack/flag"
)

var (
	masterAddr *net.TCPAddr

	localAddr    = flag.String("listen", ":9999", "local address")
	sentinelAddr = flag.String("sentinel", ":26379", "remote address")
	masterName   = flag.String("master", "", "name of the master redis node")
	password     = flag.String("password", "", "password (if any) to authenticate")
	debug        = flag.Bool("debug", false, "sets debug mode")
)

func main() {
	flag.Parse()

	laddr, err := net.ResolveTCPAddr("tcp", *localAddr)
	if err != nil {
		log.Fatalf("Failed to resolve local address: %s", err)
	}

	stopChan := make(chan string)
	go master(&stopChan)

	listener, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			log.Println(err)
			continue
		}
		go proxy(conn, masterAddr, stopChan)
	}
}

func master(stopChan *chan string) {
	var err error
	var possibleMaster *net.TCPAddr
	for {
		// has master changed from last time?
		possibleMaster, err = getMasterAddr(*sentinelAddr, *masterName, *password)
		if err != nil {
			log.Printf("[MASTER] Error polling for new master: %s\n", err)
		} else {
			if possibleMaster != nil && possibleMaster.String() != masterAddr.String() {
				log.Printf("[MASTER] Master Address changed from %s to %s \n", masterAddr.String(), possibleMaster.String())
				masterAddr = possibleMaster
				close(*stopChan)
				*stopChan = make(chan string)
			}
		}

		if masterAddr == nil {
			// if we haven't discovered a master at all, then slow our roll as the cluster is
			// probably still coming up
			time.Sleep(1 * time.Second)
		} else {
			// if we've seen a master before, then it's time for beast mode
			time.Sleep(250 * time.Millisecond)
		}

	}
}

func pipe(r net.Conn, w net.Conn, proxyChan chan<- string) {
	bytes, err := io.Copy(w, r)
	log.Printf("[PROXY %s => %s] Shutting down stream; transferred %v bytes: %v\n", w.RemoteAddr().String(), r.RemoteAddr().String(), bytes, err)
	close(proxyChan)
}

// pass a stopChan to the go routtine
func proxy(client *net.TCPConn, redisAddr *net.TCPAddr, stopChan <-chan string) {
	redis, err := net.DialTimeout("tcp", redisAddr.String(), 50*time.Millisecond)
	if err != nil {
		log.Printf("[PROXY %s => %s] Can't establish connection: %s\n", client.RemoteAddr().String(), redisAddr.String(), err)
		client.Close()
		return
	}

	log.Printf("[PROXY %s => %s] New connection\n", client.RemoteAddr().String(), redisAddr.String())
	defer client.Close()
	defer redis.Close()

	clientChan := make(chan string)
	redisChan := make(chan string)

	go pipe(client, redis, redisChan)
	go pipe(redis, client, clientChan)

	select {
	case <-stopChan:
	case <-clientChan:
	case <-redisChan:
	}

	log.Printf("[PROXY %s => %s] Closing connection\n", client.RemoteAddr().String(), redisAddr.String())
}

func getMasterAddr(sentinelAddress string, masterName string, password string) (*net.TCPAddr, error) {

	sentinelHost, sentinelPort, err := net.SplitHostPort(sentinelAddress)
	if err != nil {
		return nil, fmt.Errorf("Can't find Sentinel: %s", err)
	}

	sentinels, err := net.LookupIP(sentinelHost)
	if err != nil {
		return nil, fmt.Errorf("Can't lookup Sentinel: %s", err)
	}

	for _, sentinelIP := range sentinels {
		sentineladdr := net.JoinHostPort(sentinelIP.String(), senintelPort);
		conn, err := net.DialTimeout("tcp", sentineladdr, 100*time.Millisecond)
		if err != nil {
			log.Printf("[MASTER] Unable to connect to Sentinel at %v:%v: %v", sentinelIP, sentinelPort, err)
			continue
		}
		defer conn.Close()

		if len(password) > 0 {
			conn.Write([]byte(fmt.Sprintf("AUTH %s\n", password)))
			if *debug {
				fmt.Println("> AUTH ", password)
			}
			authResp := make([]byte, 256)
			_, err = conn.Read(authResp)
		
			if *debug {
				fmt.Println("< ", string(authResp))
			}
		}

		if *debug {
			fmt.Println("> sentinel get-master-addr-by-name ", masterName)
		}
		conn.Write([]byte(fmt.Sprintf("sentinel get-master-addr-by-name %s\n", masterName)))

		b := make([]byte, 256)
		_, err = conn.Read(b)
		if err != nil {
			log.Printf("[MASTER] Error reading from Sentinel %v:%v: %s", sentinelIP, sentinelPort, err)
		}

		parts := strings.Split(string(b), "\r\n")
		if *debug {
			fmt.Println("< ", string(b))
		}
	
		if len(parts) < 5 {
			log.Printf("[MASTER] Unexpected response from Sentinel %v:%v: %s", sentinelIP, sentinelPort, string(b))
			continue
		}

		//getting the string address for the master node
		stringaddr := net.JoinHostPort(parts[2], parts[4])
		addr, err := net.ResolveTCPAddr("tcp", stringaddr)
		if err != nil {
			log.Printf("[MASTER] Unable to resolve new master (from %s:%s) %s: %s", sentinelIP, sentinelPort, stringaddr, err)
			continue
		}

		//check that there's actually someone listening on that address
		conn2, err := net.DialTimeout("tcp", addr.String(), 50*time.Millisecond)
		if err != nil {
			log.Printf("[MASTER] Error checking new master (from %s:%s) %s: %s", sentinelIP, sentinelPort, stringaddr, err)
			continue
		}
		defer conn2.Close()

		return addr, err
	}

	return nil, fmt.Errorf("No Sentinels returned a valid master: %v", sentinels)

}
