package meter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Snapshot is the parsed state of the LP-100A at a single poll instant.
// JSON tags define the wire shape sent to clients.
type Snapshot struct {
	Timestamp     time.Time `json:"-"`
	PowerW        float64   `json:"power_w"`
	ZOhm          float64   `json:"z_ohm"`
	PhaseDeg      float64   `json:"phase_deg"`
	SWR           float64   `json:"swr"`
	DBm           float64   `json:"dbm"`
	DBW           float64   `json:"dbw"`
	Range         string    `json:"range"`          // high | mid | low
	Mode          string    `json:"mode"`           // average | peak_hold | fast
	AlarmSetpoint string    `json:"alarm_setpoint"` // off | 1.5 | 2.0 | 2.5 | 3.0 | user
	AlarmTripped  bool      `json:"alarm_tripped"`
	Callsign      string    `json:"callsign"`
}

var (
	rangeNames = map[byte]string{'0': "high", '1': "mid", '2': "low"}
	modeNames  = map[byte]string{'0': "average", '1': "peak_hold", '2': "fast"}
	alarmNames = map[byte]string{'0': "off", '1': "1.5", '2': "2.0", '3': "2.5", '4': "3.0", '5': "user"}
)

var alarmThresholds = map[string]float64{
	"1.5": 1.5, "2.0": 2.0, "2.5": 2.5, "3.0": 3.0,
}

// Parse decodes a single LP-100A poll response.
// Format: ';<power>,<z>,<phase>,<alarm>,<callsign>,<range>,<peakhold>,<dbm>,<swr>'
// We tolerate echo prefixes by anchoring on the LAST ';'.
func Parse(line string) (Snapshot, error) {
	line = strings.TrimRight(line, "\r\n\x00 \t")
	if i := strings.LastIndex(line, ";"); i >= 0 {
		line = line[i:]
	} else {
		return Snapshot{}, errors.New("no ';' in frame")
	}
	if len(line) < 30 {
		return Snapshot{}, fmt.Errorf("frame too short (%d bytes)", len(line))
	}

	parts := strings.Split(line[1:], ",")
	if len(parts) != 9 {
		return Snapshot{}, fmt.Errorf("expected 9 csv fields, got %d", len(parts))
	}

	var (
		s   Snapshot
		err error
	)
	if s.PowerW, err = parseFloat(parts[0]); err != nil {
		return s, fmt.Errorf("power: %w", err)
	}
	if s.ZOhm, err = parseFloat(parts[1]); err != nil {
		return s, fmt.Errorf("z: %w", err)
	}
	if s.PhaseDeg, err = parseFloat(parts[2]); err != nil {
		return s, fmt.Errorf("phase: %w", err)
	}
	s.AlarmSetpoint = lookup(alarmNames, parts[3], "off")
	s.Callsign = strings.TrimSpace(parts[4])
	s.Range = lookup(rangeNames, parts[5], "high")
	s.Mode = lookup(modeNames, parts[6], "average")
	if s.DBm, err = parseFloat(parts[7]); err != nil {
		return s, fmt.Errorf("dbm: %w", err)
	}
	if s.SWR, err = parseFloat(parts[8]); err != nil {
		return s, fmt.Errorf("swr: %w", err)
	}

	s.DBW = s.DBm - 30
	if s.DBW < 0 {
		s.DBW = 0
	}

	if t, ok := alarmThresholds[s.AlarmSetpoint]; ok {
		s.AlarmTripped = s.SWR > t
	}

	s.Timestamp = time.Now().UTC()
	return s, nil
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func lookup(m map[byte]string, field, def string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return def
	}
	if v, ok := m[field[0]]; ok {
		return v
	}
	return def
}

// CloseEnough returns true if two snapshots are equivalent for broadcast
// purposes. Small float jitter is suppressed via per-field deadbands so we
// don't fan out a frame on every polling cycle when the radio is keyed.
func (s Snapshot) CloseEnough(o Snapshot) bool {
	return floatNear(s.PowerW, o.PowerW, 0.05) &&
		floatNear(s.ZOhm, o.ZOhm, 0.1) &&
		floatNear(s.PhaseDeg, o.PhaseDeg, 0.1) &&
		floatNear(s.SWR, o.SWR, 0.005) &&
		floatNear(s.DBm, o.DBm, 0.05) &&
		s.Range == o.Range &&
		s.Mode == o.Mode &&
		s.AlarmSetpoint == o.AlarmSetpoint &&
		s.AlarmTripped == o.AlarmTripped &&
		s.Callsign == o.Callsign
}

func floatNear(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
