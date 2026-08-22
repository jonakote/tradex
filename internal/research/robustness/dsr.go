package robustness

import (
	"errors"
	"math"
)

const eulerMascheroni = 0.5772156649015329

func calculateDeflatedSharpe(returns, trialSharpes []float64, independentTrials float64) (DeflatedSharpe, error) {
	if len(returns) < 3 {
		return DeflatedSharpe{}, errors.New("Deflated Sharpe requires at least three returns")
	}
	mean, sampleDeviation, skewness, kurtosis, err := returnMoments(returns)
	if err != nil {
		return DeflatedSharpe{}, err
	}
	observedSharpe := mean / sampleDeviation
	trialVariance := populationVariance(trialSharpes)
	expectedMaximum := 0.0
	if independentTrials > 1 && trialVariance > 0 {
		first := inverseNormalCDF(1 - 1/independentTrials)
		second := inverseNormalCDF(1 - 1/(independentTrials*math.E))
		expectedMaximum = math.Sqrt(trialVariance) * ((1-eulerMascheroni)*first + eulerMascheroni*second)
	}
	denominatorSquared := 1 - skewness*observedSharpe +
		((kurtosis-1)/4)*observedSharpe*observedSharpe
	if denominatorSquared <= 0 || !finite(denominatorSquared) {
		return DeflatedSharpe{}, errors.New("Deflated Sharpe denominator is not positive")
	}
	z := (observedSharpe - expectedMaximum) * math.Sqrt(float64(len(returns)-1)) / math.Sqrt(denominatorSquared)
	probability := normalCDF(z)
	if !finite(probability) {
		return DeflatedSharpe{}, errors.New("Deflated Sharpe probability is not finite")
	}
	return DeflatedSharpe{
		ReturnUnit:         "PER_TRADE_EXCESS_RETURN",
		SharpeEstimator:    "MEAN_OVER_SAMPLE_STANDARD_DEVIATION_V1",
		MomentEstimator:    "POPULATION_CENTRAL_MOMENTS_V1",
		SelectionThreshold: "EXPECTED_MAXIMUM_SHARPE_BAILEY_LOPEZ_DE_PRADO_V1",
		SampleCount:        len(returns), ObservedSharpe: observedSharpe,
		Skewness: skewness, Kurtosis: kurtosis, TotalTrials: len(trialSharpes),
		IndependentTrials: independentTrials, TrialSharpeVariance: trialVariance,
		ExpectedMaximumSharpe: expectedMaximum, DeflatedSharpeProbability: probability,
	}, nil
}

func returnMoments(values []float64) (mean, sampleDeviation, skewness, kurtosis float64, err error) {
	for _, value := range values {
		if !finite(value) {
			return 0, 0, 0, 0, errors.New("returns must be finite")
		}
		mean += value
	}
	mean /= float64(len(values))
	var sum2, sum3, sum4 float64
	for _, value := range values {
		delta := value - mean
		delta2 := delta * delta
		sum2 += delta2
		sum3 += delta2 * delta
		sum4 += delta2 * delta2
	}
	if sum2 <= 0 {
		return 0, 0, 0, 0, errors.New("returns must have positive variance")
	}
	sampleDeviation = math.Sqrt(sum2 / float64(len(values)-1))
	populationDeviation := math.Sqrt(sum2 / float64(len(values)))
	skewness = (sum3 / float64(len(values))) / math.Pow(populationDeviation, 3)
	kurtosis = (sum4 / float64(len(values))) / math.Pow(populationDeviation, 4)
	return mean, sampleDeviation, skewness, kurtosis, nil
}

func populationVariance(values []float64) float64 {
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	return variance / float64(len(values))
}

func normalCDF(value float64) float64 {
	return 0.5 * math.Erfc(-value/math.Sqrt2)
}

// inverseNormalCDF uses Peter J. Acklam's rational approximation. The final
// Halley refinement brings ordinary double-precision inputs close to machine
// precision without adding a third-party numerical dependency.
func inverseNormalCDF(probability float64) float64 {
	if probability <= 0 {
		return math.Inf(-1)
	}
	if probability >= 1 {
		return math.Inf(1)
	}
	a := [...]float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
		1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := [...]float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
		6.680131188771972e+01, -1.328068155288572e+01}
	c := [...]float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
		-2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := [...]float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00,
		3.754408661907416e+00}
	const lower = 0.02425
	const upper = 1 - lower
	var estimate float64
	if probability < lower {
		q := math.Sqrt(-2 * math.Log(probability))
		estimate = (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	} else if probability <= upper {
		q := probability - 0.5
		r := q * q
		estimate = (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	} else {
		q := math.Sqrt(-2 * math.Log(1-probability))
		estimate = -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
	errorValue := normalCDF(estimate) - probability
	correction := errorValue * math.Sqrt(2*math.Pi) * math.Exp(estimate*estimate/2)
	return estimate - correction/(1+estimate*correction/2)
}
