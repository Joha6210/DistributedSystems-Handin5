package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	proto "main/grpc"
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
	proto.UnimplementedAuctionServer
	port       string
	id         int32
	clk        uint32
	bidders    map[string]string
	currentBid int32
	isOver     bool
	backend    *BackendClient
}

type BackendServer struct {
	proto.UnimplementedBackendServer
	port      string
	id        string
	clk       uint32
	isLeader  bool
	advServer *zeroconf.Server
	mu        sync.Mutex
}

type BackendClient struct {
	id             string
	clk            uint32
	replicas       map[string]proto.BackendClient
	seen           map[string]bool
	mu             sync.Mutex
	leader         proto.BackendClient
	leaderId       string
	isLeader       bool
	lastLeaderSeen time.Time
	server         BackendServer
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
		id, _ := strconv.Atoi(os.Args[2])
		server.id = int32(id)
		backend.id = os.Args[2]
	}
	if len(os.Args) > 3 {
		backend.port = os.Args[3]
	}
	if len(os.Args) > 4 {
		backend.isLeader, _ = strconv.ParseBool(os.Args[4])
	}

	backendClient := &BackendClient{
		id:       backend.id,
		replicas: make(map[string]proto.BackendClient),
		seen:     make(map[string]bool),
		isLeader: backend.isLeader}

	server.backend = backendClient

	//Start up and configure logging output to file
	f, err := os.OpenFile("logs/server"+fmt.Sprintf("%d", server.id)+"log"+time.Now().Format("20060102150405")+".log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal(err)
	}

	//defer to close when we are done with it.
	defer f.Close()

	//set output of logs to f
	log.SetOutput(f)
	log.Printf("[%d]: Started logging", server.id)

	go server.start_server()
	go backend.start_backend()

	backendClient.server = *backend

	backendClient.startPeerDiscovery() //Run in background to discover new server nodes

	for {
		time.Sleep(1000 * time.Millisecond)
	}

}

func (s *AuctionServer) start_server() {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", s.port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	proto.RegisterAuctionServer(grpcServer, s)
	log.Printf("Frontend: gRPC server now listening on %s... at logical time: %d \n", s.port, s.clk)
	grpcServer.Serve(listener)

	defer grpcServer.Stop()
}

func (s *AuctionServer) Bid(ctx context.Context, amount *proto.Amount) (*proto.Ack, error) {
	var result proto.Ack
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

func (s *AuctionServer) UpdateBid(amount int32) *proto.Ack {
	var result proto.Ack
	if amount > s.currentBid {
		s.currentBid = amount

		//Sync with other instances of server
		result = proto.Ack{Ack: proto.AckTypes_SUCCESS}
		return &result
	} else {
		result = proto.Ack{Ack: proto.AckTypes_FAIL}
		return &result
	}
}

func (s *AuctionServer) Result(ctx context.Context, empty *emptypb.Empty) (*proto.AuctionResult, error) {

	return nil, errors.New("not implemented yet")
}

func (b *BackendServer) start_backend() {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", b.port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	proto.RegisterBackendServer(grpcServer, b)
	log.Printf("Backend: gRPC server now listening on %s... at logical time: %d \n", b.port, b.clk)
	b.startAdvertising()
	grpcServer.Serve(listener)

	defer grpcServer.Stop()
}

func (b *BackendServer) UpdateLeaderStatus(isLeader bool) {
	b.isLeader = isLeader
	go b.startAdvertising()
}

// startAdvertising registers zeroconf and keeps a reference
func (b *BackendServer) startAdvertising() {
	txt := []string{
		"nodeID=" + b.id,
		"isLeader=" + fmt.Sprintf("%t", b.isLeader),
	}

	port, _ := strconv.Atoi(b.port)

	server, err := zeroconf.Register(
		"node-"+b.id,
		"_auctionBackend._tcp",
		"local.",
		port,
		txt,
		nil,
	)
	if err != nil {
		log.Fatalf("Failed to advertise node %s: %v", b.id, err)
	}
	b.mu.Lock()
	b.advServer = server
	b.mu.Unlock()
	log.Printf("[Node %s] Advertised on network (port %d) as leader? %t", b.id, port, b.isLeader)
}

func (c *BackendClient) sendUpdateToReplicas(amount int32) proto.AckTypes {
	amtMessage := proto.Amount{Clock: int32(c.clk), Amount: amount}
	var noOfReplicas int = len(c.replicas)
	replySuccessCount := 0
	for _, v := range c.replicas {
		ack, err := v.TryToUpdateBid(context.Background(), &amtMessage)
		if err != nil {
			log.Printf("Could not communicate with replica %s", err)
		}

		if ack.Ack == proto.AckTypes_SUCCESS {
			replySuccessCount++
		}
	}

	if replySuccessCount == noOfReplicas {
		return proto.AckTypes_SUCCESS
	} else {
		return proto.AckTypes_FAIL
	}
}

func (c *BackendClient) startPeerDiscovery() {
	log.Printf("[%s]: Starting peer discovery...", c.id)

	// Run a separate goroutine for leader heartbeat check
	go func() {
		for {
			time.Sleep(10 * time.Second)
			c.mu.Lock()
			leaderClient := c.leader
			leaderID := c.leaderId
			c.mu.Unlock()

			if c.leader != nil {
				_, err := leaderClient.Ping(context.Background(), &emptypb.Empty{})
				if err != nil {
					log.Printf("[Node %s] Leader %s unresponsive, triggering election (%v)", c.id, c.leaderId, err)
					c.mu.Lock()
					delete(c.replicas, leaderID)
					c.leader = nil
					c.leaderId = ""
					c.mu.Unlock()
					c.callForElection(c.server)
				} else {
					c.mu.Lock()
					c.lastLeaderSeen = time.Now() // update heartbeat
					c.mu.Unlock()
				}
			}
		}
	}()

	// Continuous peer discovery
	go func() {
		for {
			discovered := make(chan ServerNodeInfo)
			go c.discoverBackendNodes(discovered)

			for peer := range discovered {
				// Check if replica exists under lock
				c.mu.Lock()
				_, exists := c.replicas[peer.nodeId]
				c.mu.Unlock()
				if exists {
					continue
				}

				// Dial outside the lock (blocking call)
				conn, err := grpc.NewClient(peer.address, grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					log.Printf("[Node %s] Failed to connect to %s: %v", c.id, peer.address, err)
					continue
				}
				client := proto.NewBackendClient(conn)
				c.mu.Lock()
				c.replicas[peer.nodeId] = client

				// Leader detection & heartbeat
				if peer.isLeader {
					if c.leader == nil {
						c.leader = c.replicas[peer.nodeId]
						c.leaderId = peer.nodeId
						c.lastLeaderSeen = time.Now()
						log.Printf("[Node %s] New leader found %s (%s)", c.id, peer.nodeId, peer.address)
					} else if c.leader != nil && peer.nodeId == c.leaderId {
						c.lastLeaderSeen = time.Now()
						log.Printf("[Node %s] Leader still alive %s (%s)", c.id, peer.nodeId, peer.address)
					}
				}

				c.mu.Unlock()
			}

			time.Sleep(5 * time.Second) // discovery interval
		}
	}()
}

func (c *BackendClient) discoverBackendNodes(discovered chan<- ServerNodeInfo) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Fatalf("[Node %s] Failed to initialize resolver: %v", c.id, err)
	}

	entries := make(chan *zeroconf.ServiceEntry)

	go func(results <-chan *zeroconf.ServiceEntry) {
		for entry := range results {
			// skip self
			if entry.Instance == fmt.Sprintf("node-%s", c.id) || len(entry.AddrIPv4) == 0 {
				continue
			}

			addr := fmt.Sprintf("%s:%d", entry.AddrIPv4[0].String(), entry.Port)

			peerID := entry.Instance[len("node-"):]
			isLeaderTXT := false

			for _, txt := range entry.Text {
				if strings.HasPrefix(txt, "isLeader=") {
					val := strings.TrimPrefix(txt, "isLeader=")
					isLeaderTXT = (val == "true")
					break
				}
			}

			log.Printf("[Node %s] Discovered server node: %s (%s) leader=%v", c.id, peerID, addr, isLeaderTXT)
			discovered <- ServerNodeInfo{nodeId: peerID, address: addr, isLeader: isLeaderTXT}
		}
	}(entries)

	// Long-lived browse context
	ctx := context.Background()
	if err := resolver.Browse(ctx, "_auctionBackend._tcp", "local.", entries); err != nil {
		log.Fatalf("[%s] Failed to browse: %v", c.id, err)
	}
}

func (c *BackendClient) callForElection(backendServer BackendServer) {
	c.mu.Lock()
	nodeId, _ := strconv.Atoi(c.id)
	clk := c.clk
	if len(c.replicas) <= 0 {
		log.Printf("[Node %s] No other replicas in network, I will promote myself to leader! ", c.id)
		c.isLeader = true
		backendServer.UpdateLeaderStatus(true)
		c.mu.Unlock()
		return
	} else {
		// Copy replicas map to slice
		replicas := make([]proto.BackendClient, 0, len(c.replicas))
		for _, r := range c.replicas {
			replicas = append(replicas, r)
		}
		c.mu.Unlock()

		log.Printf("[Node %d] Calling an election between %d nodes", c.id, len(replicas))
		for replicaId, replica := range replicas {
			if replicaId <= nodeId {
				// Skip replicas with lower or equal IDs
				continue
			}
			go func(r proto.BackendClient) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := r.Election(ctx, &proto.Message{Id: fmt.Sprintf("%d", nodeId), Clock: int32(clk)})
				if err != nil {
					log.Printf("[Node %d] Failed to call Election on replica: %v", c.id, err)
				} else {
					log.Printf("[Node %d] Successfully called Election on replica", c.id)
				}
			}(replica)
		}
	}

}

func (b *BackendServer) Ping(ctx context.Context, empty *emptypb.Empty) (*proto.Answer, error) {
	log.Printf("[Node %s] got pinged, returning ok!", b.id)
	// Simply return OK
	return &proto.Answer{Clock: int32(b.clk), Id: b.id}, nil
}

func (b *BackendServer) Election(ctx context.Context, msg *proto.Message) (*proto.Answer, error) {

	return nil, nil
}
