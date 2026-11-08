package oee

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// The cases live in contracts/oee/cases.json, shared with the pytest of
// analytics/etl-oee, so both implementations follow the same rules (ADR 0009).
type casesFile struct {
	Cases []struct {
		Name        string  `json:"name"`
		PlannedS    float64 `json:"planned_s"`
		IdealCycleS float64 `json:"ideal_cycle_s"`
		Readings    []struct {
			T          float64        `json:"t"`
			BootID     uint32         `json:"boot_id"`
			State      contract.State `json:"state"`
			GoodTotal  uint64         `json:"good_total"`
			ScrapTotal uint64         `json:"scrap_total"`
		} `json:"readings"`
		Want struct {
			RunningS     float64 `json:"running_s"`
			Good         uint64  `json:"good"`
			Scrap        uint64  `json:"scrap"`
			Availability float64 `json:"availability"`
			Performance  float64 `json:"performance"`
			Quality      float64 `json:"quality"`
			OEE          float64 `json:"oee"`
		} `json:"want"`
	} `json:"cases"`
}

var start = time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func TestCompute(t *testing.T) {
	data, err := os.ReadFile("../../contracts/oee/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f casesFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var readings []Reading
			for _, r := range c.Readings {
				readings = append(readings, Reading{BootID: r.BootID, TS: start.Add(seconds(r.T)), State: r.State,
					GoodTotal: r.GoodTotal, ScrapTotal: r.ScrapTotal})
			}
			got := Compute(readings, seconds(c.PlannedS), seconds(c.IdealCycleS))
			w := c.Want
			want := Result{
				Planned: seconds(c.PlannedS), Running: seconds(w.RunningS), Good: w.Good, Scrap: w.Scrap,
				Availability: w.Availability, Performance: w.Performance, Quality: w.Quality, OEE: w.OEE,
			}
			if !equal(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

// equal compares results, allowing for float rounding in the factors.
func equal(a, b Result) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }
	return a.Planned == b.Planned && a.Running == b.Running && a.Good == b.Good && a.Scrap == b.Scrap &&
		near(a.Availability, b.Availability) && near(a.Performance, b.Performance) &&
		near(a.Quality, b.Quality) && near(a.OEE, b.OEE)
}
