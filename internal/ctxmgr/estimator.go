package ctxmgr

// Estimator turns a raw chars/4 estimate into a calibrated token count,
// and tracks assembly pressure against the effective window (§3a "the
// meter's estimator; bounded error is fine for budgeting — provider usage
// via Observe() recalibrates a per-model correction factor").
type Estimator struct {
	// correction is a multiplicative factor applied to the raw chars/4
	// estimate: correction = actual_usage / raw_estimate, EWMA'd across
	// Observe calls so a single noisy request can't swing the gauge.
	correction float64
	observed   bool
}

// NewEstimator starts with correction 1.0 (trust the raw estimate until
// Observe() calibrates it).
func NewEstimator() *Estimator {
	return &Estimator{correction: 1.0}
}

// EstimateTokens is the raw chars/4 estimate, calibrated.
func (e *Estimator) EstimateTokens(chars int) int64 {
	raw := float64(chars) / float64(BytesPerToken)
	return int64(raw * e.correction)
}

// Observe folds one request's actual usage against the raw estimate for
// the same assembly (rawChars, the char count the estimate was computed
// from) into the correction factor. An EWMA (alpha 0.3) so the gauge
// adapts without thrashing on one outlier request.
func (e *Estimator) Observe(rawChars int, actualTokens int64) {
	if rawChars <= 0 || actualTokens <= 0 {
		return
	}
	sample := float64(actualTokens) / (float64(rawChars) / float64(BytesPerToken))
	const alpha = 0.3
	if !e.observed {
		e.correction = sample
		e.observed = true
		return
	}
	e.correction = e.correction*(1-alpha) + sample*alpha
}
