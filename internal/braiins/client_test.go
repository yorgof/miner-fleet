package braiins

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mockMiner(t *testing.T, replies map[string]string) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				var request struct {
					Command string `json:"command"`
				}
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					return
				}
				body, ok := replies[request.Command]
				if !ok {
					body = `{"STATUS":[{"STATUS":"E"}]}`
				}
				// Keep the socket open after the NUL: the client must not wait for EOF.
				conn.Write(append([]byte(body), 0))
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	return New(host, port)
}

func TestBOSminerMonitoring(t *testing.T) {
	c := mockMiner(t, map[string]string{
		"summary":     `{"STATUS":[{"STATUS":"S"}],"SUMMARY":[{"MHS 5s":8500000,"MHS av":9000000,"Elapsed":3600,"Accepted":42,"Rejected":2,"Best Share":12345,"Found Blocks":0}]}`,
		"temps":       `{"STATUS":[{"STATUS":"S"}],"TEMPS":[{"Chip":70,"Board":50},{"Chip":72,"Board":51}]}`,
		"fans":        `{"STATUS":[{"STATUS":"S"}],"FANS":[{"RPM":1200,"Speed":25},{"RPM":1300,"Speed":25}]}`,
		"tunerstatus": `{"STATUS":[{"STATUS":"S"}],"TUNERSTATUS":[{"ApproximateMinerPowerConsumption":600,"PowerLimit":650}]}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := c.FetchStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.HashrateGHs != 8500 || s.TempC != 72 || s.FanRPM != 1300 || s.FanPercent != 25 || s.PowerW != 600 || s.SharesAccepted != 42 || s.SharesRejected != 2 || s.UptimeS != 3600 || s.BestDiff != 12345 {
		t.Fatalf("wrong decoded stats: %+v", s)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.Raw), &raw); err != nil || len(raw) != 4 {
		t.Fatalf("raw history: %s %v", s.Raw, err)
	}
}

func TestSummarySurvivesMissingOptionalCommands(t *testing.T) {
	c := mockMiner(t, map[string]string{
		"summary": `{"STATUS":[{"STATUS":"S"}],"SUMMARY":[{"MHS av":"10000000","Accepted":0,"Elapsed":12}]}`,
	})
	s, err := c.FetchStats(context.Background())
	if err != nil || s.HashrateGHs != 10000 || s.PowerW != 0 || s.TempC != 0 {
		t.Fatalf("summary lost when sensors unavailable: %+v %v", s, err)
	}
}

func TestSummaryFailure(t *testing.T) {
	for _, body := range []string{
		`{"STATUS":[{"STATUS":"E"}],"SUMMARY":[{"MHS av":10000000}]}`,
		`{"STATUS":[{"STATUS":"S"}],"SUMMARY":[]}`,
		`{"STATUS":[{"STATUS":"S"}],"SUMMARY":[{}]}`,
		`not JSON`,
	} {
		c := mockMiner(t, map[string]string{"summary": body})
		if _, err := c.FetchStats(context.Background()); err == nil {
			t.Errorf("accepted broken summary: %s", body)
		}
	}
}

func TestUnavailableMiner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New("192.0.2.1", 0).FetchStats(ctx)
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("unavailable miner: %v", err)
	}
}
