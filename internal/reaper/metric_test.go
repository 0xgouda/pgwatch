package reaper

import (
	"context"
	"testing"

	"github.com/cybertec-postgresql/pgwatch/v3/internal/cmdopts"
	"github.com/cybertec-postgresql/pgwatch/v3/internal/log"
	"github.com/cybertec-postgresql/pgwatch/v3/internal/metrics"
	"github.com/cybertec-postgresql/pgwatch/v3/internal/sources"
	"github.com/cybertec-postgresql/pgwatch/v3/internal/testutil"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
)

var (
	initialMetricDefs = metrics.MetricDefs{
		"metric1": metrics.Metric{Description: "metric1"},
	}
	initialPresetDefs = metrics.PresetDefs{
		"preset1": metrics.Preset{Description: "preset1", Metrics: map[string]float64{"metric1": 1.0}},
	}

	newMetricDefs = metrics.MetricDefs{
		"metric2": metrics.Metric{Description: "metric2"},
	}
	newPresetDefs = metrics.PresetDefs{
		"preset2": metrics.Preset{Description: "preset2", Metrics: map[string]float64{"metric2": 2.0}},
	}
)

func TestReaper_FetchStatsDirectlyFromOS(t *testing.T) {
	a := assert.New(t)
	r := &Reaper{}
	conn, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	expq := conn.ExpectQuery("SELECT COALESCE(inet_client_addr(), inet_server_addr()) IS NULL")
	expq.Times(uint(len(directlyFetchableOSMetrics)))
	md := &sources.SourceConn{Conn: conn}
	for _, m := range directlyFetchableOSMetrics {
		expq.WillReturnRows(pgxmock.NewRows([]string{"is_unix_socket"}).AddRow(true))
		a.True(IsDirectlyFetchableMetric(md, m), "Expected %s to be directly fetchable", m)
		a.NotPanics(func() {
			_, _ = r.FetchStatsDirectlyFromOS(context.Background(), md, m)
		})
	}
}

func TestConcurrentMetricDefs_Assign(t *testing.T) {
	concurrentDefs := NewConcurrentMetricDefs()
	concurrentDefs.Assign(&metrics.Metrics{
		MetricDefs: initialMetricDefs,
		PresetDefs: initialPresetDefs,
	})

	concurrentDefs.Assign(&metrics.Metrics{
		MetricDefs: newMetricDefs,
		PresetDefs: newPresetDefs,
	})

	assert.Equal(t, newMetricDefs, concurrentDefs.MetricDefs, "MetricDefs should be updated")
	assert.Equal(t, newPresetDefs, concurrentDefs.PresetDefs, "PresetDefs should be updated")
}

func TestConcurrentMetricDefs_RandomAccess(t *testing.T) {
	a := assert.New(t)

	concurrentDefs := NewConcurrentMetricDefs()
	concurrentDefs.Assign(&metrics.Metrics{
		MetricDefs: initialMetricDefs,
		PresetDefs: initialPresetDefs,
	})

	go a.NotPanics(func() {
		for range 1000 {
			_, ok1 := concurrentDefs.GetMetricDef("metric1")
			_, ok2 := concurrentDefs.GetMetricDef("metric2")
			a.True(ok1 || ok2, "Expected metric1 or metric3 to exist at any time")
			_, ok1 = concurrentDefs.GetPresetDef("preset1")
			_, ok2 = concurrentDefs.GetPresetDef("preset2")
			a.True(ok1 || ok2, "Expected preset1 or preset2 to exist at any time")
			m1 := concurrentDefs.GetPresetMetrics("preset1")
			m2 := concurrentDefs.GetPresetMetrics("preset2")
			a.True(m1 != nil || m2 != nil, "Expected preset1 or preset2 metrics to be non-empty")
		}
	})

	go a.NotPanics(func() {
		for range 1000 {
			concurrentDefs.Assign(&metrics.Metrics{
				MetricDefs: newMetricDefs,
				PresetDefs: newPresetDefs,
			})
		}
	})
}

func TestReaper_LoadMetrics(t *testing.T) {
	ctx := log.WithLogger(context.Background(), log.NewNoopLogger())

	testMetrics := &metrics.Metrics{
		MetricDefs: metrics.MetricDefs{
			"cpu_load":  metrics.Metric{Description: "CPU load metric"},
			"mem_usage": metrics.Metric{Description: "Memory usage metric"},
		},
		PresetDefs: metrics.PresetDefs{
			"basic": metrics.Preset{
				Description: "Basic preset",
				Metrics:     map[string]float64{"cpu_load": 10.0, "mem_usage": 30.0},
			},
			"standby": metrics.Preset{
				Description: "Standby preset",
				Metrics:     map[string]float64{"cpu_load": 60.0},
			},
		},
	}

	mockReader := &testutil.MockMetricsReaderWriter{
		GetMetricsFunc: func() (*metrics.Metrics, error) {
			return testMetrics, nil
		},
	}

	r := NewReaper(ctx, &cmdopts.Options{MetricsReaderWriter: mockReader})

	r.monitoredSources = sources.SourceConns{
		sources.NewSourceConn(sources.Source{
			Name:                 "source1",
			PresetMetrics:        "basic",
			PresetMetricsStandby: "standby",
		}),
		sources.NewSourceConn(sources.Source{
			Name:                 "source2",
			PresetMetrics:        "basic",
			PresetMetricsStandby: "",
		}),
	}

	// Reset metricDefs before test
	metricDefs = NewConcurrentMetricDefs()

	err := r.LoadMetrics()
	assert.NoError(t, err)

	// Verify metricDefs is updated
	cpuMetric, ok := metricDefs.GetMetricDef("cpu_load")
	assert.True(t, ok, "Expected cpu_load metric to exist")
	assert.Equal(t, "CPU load metric", cpuMetric.Description)

	memMetric, ok := metricDefs.GetMetricDef("mem_usage")
	assert.True(t, ok, "Expected mem_usage metric to exist")
	assert.Equal(t, "Memory usage metric", memMetric.Description)

	basicPreset, ok := metricDefs.GetPresetDef("basic")
	assert.True(t, ok, "Expected basic preset to exist")
	assert.Equal(t, "Basic preset", basicPreset.Description)

	// Verify Metrics and MetricsStandby are correctly populated
	assert.Equal(t, map[string]float64{"cpu_load": 10.0, "mem_usage": 30.0}, r.monitoredSources[0].Metrics)
	assert.Equal(t, map[string]float64{"cpu_load": 60.0}, r.monitoredSources[0].MetricsStandby)

	assert.Equal(t, map[string]float64{"cpu_load": 10.0, "mem_usage": 30.0}, r.monitoredSources[1].Metrics)
	assert.Nil(t, r.monitoredSources[1].MetricsStandby)

	// Verify error propagation
	mockReader.GetMetricsFunc = func() (*metrics.Metrics, error) {return nil, assert.AnError}
	err = r.LoadMetrics()
	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}
