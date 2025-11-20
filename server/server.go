package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	prtAuction "main/grpc/auctionServer"
	prtBackend "main/grpc/backend"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

type AuctionServer struct {
	prtAuction.UnimplementedAuctionServer
	port       string
	id         string
	clk        uint32
	bidders    map[string]string
	currentBid int32
	isOver     bool
}

type BackendServer struct {
	prtBackend.UnimplementedBackendServer
	port     string
	id       string
	clk      uint32
	isLeader bool
	leader   int32
}

type BackendClient struct {
	id    string
	peers map[string]prtBackend.BackendClient
	seen  map[string]bool
	mu    sync.Mutex
}

type ServerNodeInfo struct {
	nodeId   string
	address  string
	isLeader bool
}

func main() {

	var server *AuctionServer = &AuctionServer{}
	server.clk = 0
	server.bidders = make(map[string]string)
	server.currentBid = 0
	server.isOver = false
	server.port = "5001" //Default port

	var backend *BackendServer = &BackendServer{}
	backend.port = "5011"
	backend.clk = 0

	if len(os.Args) > 1 {
		server.port = os.Args[1] //Override default port
	}
	if len(os.Args) > 2 {
		server.id = os.Args[2]
		backend.id = os.Args[2]
	}
	if len(os.Args) > 3 {
		backend.port = os.Args[3]
	}
	if len(os.Args) > 4 {
		backend.isLeader, _ = strconv.ParseBool(os.Args[4])
	}

	backendClient := &BackendClient{
		id:    backend.id,
		peers: make(map[string]prtBackend.BackendClient),
		seen:  make(map[string]bool)}

	//Start up and configure logging output to file
	f, err := os.OpenFile("logs/server"+server.id+"log"+time.Now().Format("20060102150405")+".log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal(err)
	}

	//defer to close when we are done with it.
	defer f.Close()

	//set output of logs to f
	log.SetOutput(f)

	go server.start_server()
	go backend.start_backend()

	backendClient.startPeerDiscovery() //Run in background to discover new server nodes

}

func (s *AuctionServer) start_server() {
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%s", s.port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	prtAuction.RegisterAuctionServer(grpcServer, s)
	log.Printf("gRPC server now listening on %s... at logical time: %d \n", s.port, s.clk)
	grpcServer.Serve(listener)

	defer grpcServer.Stop()
}

func (s *AuctionServer) Bid(ctx context.Context, amount *prtAuction.Amount) (*prtAuction.Ack, error) {
	var result prtAuction.Ack
	_, ok := s.bidders[amount.ClientId]
	if ok {
		//Already registered bidder
		result = *s.UpdateBid(amount.Amount)
	} else {
		//Register new bidder
		s.bidders[amount.ClientId] = amount.ClientId
		//Then update bid
		result = *s.UpdateBid(amount.Amount)
	}

	return &result, nil
}

func (s *AuctionServer) UpdateBid(amount int32) *prtAuction.Ack {
	var result prtAuction.Ack
	if amount > s.currentBid {
		s.currentBid = amount

		//Sync with other instances of server

		result = prtAuction.Ack{Ack: prtAuction.AckTypes_SUCCESS}
		return &result
	} else {
		result = prtAuction.Ack{Ack: prtAuction.AckTypes_FAIL}
		return &result
	}
}

func (s *AuctionServer) Result(ctx context.Context, empty *emptypb.Empty) (*prtAuction.AuctionResult, error) {

	return nil, errors.New("not implemented yet")
}

func (b *BackendServer) start_backend() {
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%s", b.port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	prtBackend.RegisterBackendServer(grpcServer, b)
	log.Printf("gRPC server now listening on %s... at logical time: %d \n", b.port, b.clk)

	port, _ := strconv.Atoi(b.port)
	go advertiseBackend(b.id, port, b.isLeader)
	grpcServer.Serve(listener)

	defer grpcServer.Stop()
}

func advertiseBackend(nodeID string, port int, isLeader bool) {
	server, err := zeroconf.Register(
		fmt.Sprintf("node-%s", nodeID), // service instance name
		"_auctionBackend._tcp",         // service type
		"local.",                       // service domain
		port,                           // service port
		[]string{"nodeID=" + nodeID, "isLeader=" + fmt.Sprintf("%b", isLeader)}, // text records
		nil, // use system interface
	)
	if err != nil {
		log.Fatalf("Failed to advertise node %s: %v", nodeID, err)
	}
	fmt.Sprintf("[Node %s] Advertised on network (port %d)", nodeID, port)
	log.Printf("[Node %s] Advertised on network (port %d)", nodeID, port)

	// Keep advertising until process exits
	defer server.Shutdown()
	select {}
}

func (c *BackendClient) startPeerDiscovery() {
	go func() {
		for {
			discovered := make(chan ServerNodeInfo)
			go discoverNodes(c.id, discovered, c.seen)

			for peer := range discovered {
				c.mu.Lock()
				if _, exists := c.peers[peer.nodeId]; !exists {
					opts := grpc.WithTransportCredentials(insecure.NewCredentials())
					conn, err := grpc.NewClient(peer.address, opts)
					if err != nil {
						log.Printf("[Node %s] Failed to connect to %s: %v", c.id, peer.address, err)
						c.mu.Unlock()
						continue
					}
					client := prtBackend.NewBackendClient(conn)
					c.peers[peer.nodeId] = client
					log.Printf("[Server node %s] Connected to new server node: %s (%s)", c.id, peer.nodeId, peer.address)
				}
				c.mu.Unlock()
			}

			time.Sleep(5 * time.Second)
		}
	}()
}

func discoverNodes(nodeID string, discovered chan<- ServerNodeInfo, seen map[string]bool) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Fatalf("[Node %s] Failed to initialize resolver: %v", nodeID, err)
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func(results <-chan *zeroconf.ServiceEntry) {
		for entry := range results {
			if entry.Instance == fmt.Sprintf("node-%s", nodeID) {
				continue // skip self
			}
			if len(entry.AddrIPv4) == 0 {
				continue
			}

			addr := fmt.Sprintf("%s:%d", entry.AddrIPv4[0].String(), entry.Port)
			if seen[addr] {
				continue
			}
			seen[addr] = true

			peerID := entry.Instance[len("node-"):] // extract numeric ID
			// --- Extract isLeader from TXT records ---
			var isLeaderTXT bool
			for _, txt := range entry.Text {
				if strings.HasPrefix(txt, "isLeader=") {
					val := strings.TrimPrefix(txt, "isLeader=")
					isLeaderTXT = (val == "true")
				}
			}

			log.Printf("[Node %s] Discovered new servernode: %s (%s)", nodeID, peerID, addr)
			discovered <- ServerNodeInfo{nodeId: peerID, address: addr, isLeader: isLeaderTXT}
		}
	}(entries)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := resolver.Browse(ctx, "_auctionBackend._tcp", "local.", entries); err != nil {
		log.Fatalf("[%s] Failed to browse: %v", nodeID, err)
	}
	<-ctx.Done()
	close(discovered)
}
