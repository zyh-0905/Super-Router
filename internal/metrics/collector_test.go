package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestReplaceChannelSuccessRatesRemovesStaleLabels(t *testing.T) {
	gauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_channel_success_rate"},
		[]string{"channel", "model"},
	)
	gauge.WithLabelValues("stale", "old-model").Set(1)

	replaceChannelSuccessRates(gauge, []channelSuccessRateSample{
		{channelID: 7, model: "current-model", successRate: 0.75},
	})

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(gauge)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("metric count = %d, want 1", metricCount(families))
	}

	labels := families[0].Metric[0].Label
	got := map[string]string{}
	for _, label := range labels {
		got[label.GetName()] = label.GetValue()
	}
	if got["channel"] != "7" || got["model"] != "current-model" {
		t.Fatalf("labels = %v, want current sample only", got)
	}
}

func metricCount(families []*dto.MetricFamily) int {
	count := 0
	for _, family := range families {
		count += len(family.Metric)
	}
	return count
}
