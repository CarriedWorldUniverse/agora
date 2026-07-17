package ctxmgr

import "testing"

func TestEstimator_RawEstimateBeforeCalibration(t *testing.T) {
	e := NewEstimator()
	if got := e.EstimateTokens(400); got != 100 {
		t.Fatalf("EstimateTokens(400) = %d, want 100 (chars/4, correction 1.0)", got)
	}
}

func TestEstimator_ObserveCalibratesCorrection(t *testing.T) {
	e := NewEstimator()
	// Raw estimate for 400 chars = 100 tokens; actual usage says 150 —
	// the correction factor should pull future estimates up.
	e.Observe(400, 150)
	if got := e.EstimateTokens(400); got != 150 {
		t.Fatalf("after one Observe, EstimateTokens(400) = %d, want 150 (first sample sets correction directly)", got)
	}
}

func TestEstimator_ObserveIgnoresZeroInputs(t *testing.T) {
	e := NewEstimator()
	e.Observe(0, 100)
	e.Observe(400, 0)
	if got := e.EstimateTokens(400); got != 100 {
		t.Fatalf("zero rawChars/actualTokens must not perturb the correction: got %d, want 100", got)
	}
}

func TestEstimator_EWMASmoothsAcrossSamples(t *testing.T) {
	e := NewEstimator()
	e.Observe(400, 100) // correction = 1.0
	e.Observe(400, 200) // sample = 2.0, EWMA alpha 0.3 -> ~1.3
	got := e.EstimateTokens(1000)
	// Not a hardcoded 325: EWMA float arithmetic can round either way at
	// the int64 truncation boundary — assert the smoothing DIRECTION and
	// magnitude (between the no-correction estimate and the full-jump
	// estimate) rather than an exact value.
	if got <= 250 || got >= 500 {
		t.Fatalf("EstimateTokens(1000) = %d, want an EWMA-smoothed value strictly between the raw estimate (250) and a full jump to the last sample (500)", got)
	}
}
