package meter

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParseExampleFrame(t *testing.T) {
	// Example response taken from the Node-RED reference flow notes.
	frame := ";1457.00,49.3,005.0,2,N8LP  ,0,2,61.6,1.02"
	s, err := Parse(frame)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !near(s.PowerW, 1457.00) {
		t.Errorf("PowerW: got %v want 1457.00", s.PowerW)
	}
	if !near(s.ZOhm, 49.3) {
		t.Errorf("ZOhm: got %v want 49.3", s.ZOhm)
	}
	if !near(s.PhaseDeg, 5.0) {
		t.Errorf("PhaseDeg: got %v want 5.0", s.PhaseDeg)
	}
	if s.AlarmSetpoint != "2.0" {
		t.Errorf("AlarmSetpoint: got %q want 2.0", s.AlarmSetpoint)
	}
	if s.Callsign != "N8LP" {
		t.Errorf("Callsign: got %q want N8LP", s.Callsign)
	}
	if s.Range != "high" {
		t.Errorf("Range: got %q want high", s.Range)
	}
	if s.Mode != "tune" {
		t.Errorf("Mode: got %q want tune", s.Mode)
	}
	if !near(s.DBm, 61.6) {
		t.Errorf("DBm: got %v want 61.6", s.DBm)
	}
	if !near(s.DBW, 61.6-30.0) {
		t.Errorf("DBW: got %v want %v", s.DBW, 61.6-30.0)
	}
	if !near(s.SWR, 1.02) {
		t.Errorf("SWR: got %v want 1.02", s.SWR)
	}
	if s.AlarmTripped {
		t.Errorf("AlarmTripped: SWR 1.02 below 2.0 setpoint should not trip")
	}
}

func TestParseRejectsShort(t *testing.T) {
	if _, err := Parse(";1,2,3"); err == nil {
		t.Errorf("expected error for short frame")
	}
}

func TestParseRejectsNoSemicolon(t *testing.T) {
	if _, err := Parse("1457.00,49.3,005.0,2,N8LP  ,0,2,61.6,1.02"); err == nil {
		t.Errorf("expected error when leading ';' missing")
	}
}

func TestParseToleratesEchoPrefix(t *testing.T) {
	// If the meter echoes the 'P' command, anchor on the last ';'.
	frame := "P;1457.00,49.3,005.0,2,N8LP  ,0,2,61.6,1.02"
	s, err := Parse(frame)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !near(s.PowerW, 1457.00) {
		t.Errorf("PowerW: got %v", s.PowerW)
	}
}

func TestParseTrimsTrailingNewline(t *testing.T) {
	frame := ";1457.00,49.3,005.0,2,N8LP  ,0,2,61.6,1.02\r\n"
	if _, err := Parse(frame); err != nil {
		t.Errorf("trailing CRLF should be tolerated: %v", err)
	}
}

func TestAlarmTrips(t *testing.T) {
	// Setpoint 1.5, SWR 2.0 -> tripped.
	frame := ";0050.00,49.3,005.0,1,N8LP  ,0,0,33.0,2.00"
	s, err := Parse(frame)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !s.AlarmTripped {
		t.Errorf("expected alarm tripped (SWR 2.0 > setpoint 1.5)")
	}
}

func TestAlarmOffNeverTrips(t *testing.T) {
	frame := ";0050.00,49.3,005.0,0,N8LP  ,0,0,33.0,5.00"
	s, _ := Parse(frame)
	if s.AlarmTripped {
		t.Errorf("alarm 'off' must never trip even at high SWR")
	}
}

func TestCloseEnoughSuppressesJitter(t *testing.T) {
	a, _ := Parse(";0100.00,49.3,005.0,0,N8LP  ,0,0,50.0,1.10")
	b, _ := Parse(";0100.02,49.3,005.0,0,N8LP  ,0,0,50.0,1.10")
	if !a.CloseEnough(b) {
		t.Errorf("0.02W jitter should be suppressed")
	}
	c, _ := Parse(";0100.00,49.3,005.0,0,N8LP  ,0,0,50.0,1.30")
	if a.CloseEnough(c) {
		t.Errorf("SWR 1.10 vs 1.30 should be reported as a change")
	}
}
