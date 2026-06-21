package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestCompareOp(t *testing.T) {
	tests := []struct {
		name      string
		value     float64
		operator  string
		threshold float64
		want      bool
	}{
		{"gt triggered", 95.0, ">", 90.0, true},
		{"gt not triggered", 90.0, ">", 90.0, false},
		{"gte triggered equal", 90.0, ">=", 90.0, true},
		{"gte triggered above", 91.0, ">=", 90.0, true},
		{"gte not triggered", 89.0, ">=", 90.0, false},
		{"lt triggered", 10.0, "<", 50.0, true},
		{"lt not triggered", 50.0, "<", 50.0, false},
		{"lte triggered equal", 50.0, "<=", 50.0, true},
		{"lte not triggered", 51.0, "<=", 50.0, false},
		{"eq triggered", 42.0, "==", 42.0, true},
		{"eq not triggered", 43.0, "==", 42.0, false},
		{"unknown operator", 10.0, "!=", 20.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareOp(tt.value, tt.operator, tt.threshold)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetNodeMetric(t *testing.T) {
	node := &model.Node{
		CPUUsage:    75.5,
		MemoryUsage: 60.0,
		DiskUsage:   45.3,
	}

	tests := []struct {
		metric string
		want   float64
	}{
		{"cpu", 75.5},
		{"memory", 60.0},
		{"disk", 45.3},
		{"unknown", -1},
	}

	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			got := getNodeMetric(node, tt.metric)
			if tt.want < 0 {
				assert.Equal(t, tt.want, got)
			} else {
				assert.InDelta(t, tt.want, got, 0.01)
			}
		})
	}
}

func TestFormatAlertMessage(t *testing.T) {
	msg := formatAlertMessage("cpu", ">", 90)
	assert.Contains(t, msg, "cpu")
	assert.Contains(t, msg, ">")
	assert.Contains(t, msg, "90")
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0, "0"},
		{90, "90"},
		{100, "100"},
		{-5, "-5"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatFloat(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{42, "42"},
		{-1, "-1"},
		{12345, "12345"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatInt(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
