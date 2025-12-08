package commander

import (
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/metorial/sentinel/internal/models"
	pb "github.com/metorial/sentinel/proto"
)

type Server struct {
	pb.UnimplementedMetricsCollectorServer
	db      *DB
	streams map[string]pb.MetricsCollector_StreamMetricsServer
	mu      sync.RWMutex
}

func NewServer(db *DB) *Server {
	return &Server{
		db:      db,
		streams: make(map[string]pb.MetricsCollector_StreamMetricsServer),
	}
}

func (s *Server) StreamMetrics(stream pb.MetricsCollector_StreamMetricsServer) error {
	ctx := stream.Context()
	log.Println("New client connected")

	var hostname string

	defer func() {
		if hostname != "" {
			s.mu.Lock()
			delete(s.streams, hostname)
			s.mu.Unlock()
			log.Printf("Removed stream for host: %s", hostname)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("Client disconnected:", ctx.Err())
			return ctx.Err()
		default:
		}

		msg, err := stream.Recv()
		if err == io.EOF {
			log.Println("Client closed stream")
			return nil
		}
		if err != nil {
			log.Printf("Error receiving message: %v", err)
			return err
		}

		switch payload := msg.Payload.(type) {
		case *pb.AgentMessage_Metrics:
			metrics := payload.Metrics
			if hostname == "" {
				hostname = metrics.Hostname
				s.mu.Lock()
				s.streams[hostname] = stream
				s.mu.Unlock()
				log.Printf("Registered stream for host: %s", hostname)
			}

			if err := s.handleMetrics(metrics); err != nil {
				log.Printf("Error handling metrics from %s: %v", metrics.Hostname, err)
				if err := stream.Send(&pb.CollectorMessage{
					Payload: &pb.CollectorMessage_Ack{
						Ack: &pb.Acknowledgment{
							Success: false,
							Message: err.Error(),
						},
					},
				}); err != nil {
					return err
				}
				continue
			}

			if err := stream.Send(&pb.CollectorMessage{
				Payload: &pb.CollectorMessage_Ack{
					Ack: &pb.Acknowledgment{
						Success: true,
						Message: "received",
					},
				},
			}); err != nil {
				return err
			}

		default:
			log.Printf("Unknown message type: %T", payload)
		}
	}
}

func (s *Server) handleMetrics(metrics *pb.HostMetrics) error {
	if metrics.Info == nil || metrics.Usage == nil {
		return fmt.Errorf("missing info or usage data")
	}

	host := &models.Host{
		Hostname:          metrics.Hostname,
		IP:                metrics.Ip,
		UptimeSeconds:     metrics.Info.UptimeSeconds,
		CPUCores:          metrics.Info.CpuCores,
		TotalMemoryBytes:  metrics.Info.TotalMemoryBytes,
		TotalStorageBytes: metrics.Info.TotalStorageBytes,
		LastSeen:          time.Unix(metrics.Timestamp, 0),
		Online:            true,
	}

	hostID, err := s.db.UpsertHost(host)
	if err != nil {
		return fmt.Errorf("upsert host: %w", err)
	}

	usage := &models.HostUsage{
		HostID:           hostID,
		Timestamp:        time.Unix(metrics.Timestamp, 0),
		CPUPercent:       metrics.Usage.CpuPercent,
		UsedMemoryBytes:  metrics.Usage.UsedMemoryBytes,
		UsedStorageBytes: metrics.Usage.UsedStorageBytes,
	}

	if err := s.db.InsertUsage(usage); err != nil {
		return fmt.Errorf("insert usage: %w", err)
	}

	return nil
}
