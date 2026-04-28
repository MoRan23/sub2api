package service

import (
	"fmt"
	"math"
)

type profitabilityOptimizationCandidate struct {
	Plan               ProfitabilityPlanItem
	UnitRevenueRMB     float64
	UnitCostUSD        float64
	UnitMarginUSD      float64
	UnitCost5hUSD      float64
	GroupConstraintIdx int
}

type profitabilityConstraintRow struct {
	Key      string
	Label    string
	Capacity float64
	Row      []float64
}

func solveLinearProgramSimplex(objective []float64, matrix [][]float64, rhs []float64) ([]float64, error) {
	if len(objective) == 0 {
		return nil, nil
	}
	if len(matrix) != len(rhs) {
		return nil, fmt.Errorf("simplex rhs length mismatch")
	}

	const eps = 1e-9
	m := len(matrix)
	n := len(objective)
	width := n + m + 1
	height := m + 1

	tableau := make([][]float64, height)
	for i := range tableau {
		tableau[i] = make([]float64, width)
	}

	basic := make([]int, m)
	for i := 0; i < m; i++ {
		if len(matrix[i]) != n {
			return nil, fmt.Errorf("simplex constraint width mismatch")
		}
		if rhs[i] < -eps {
			return nil, fmt.Errorf("simplex requires non-negative rhs")
		}
		copy(tableau[i], matrix[i])
		tableau[i][n+i] = 1
		tableau[i][width-1] = rhs[i]
		basic[i] = n + i
	}
	for j := 0; j < n; j++ {
		tableau[m][j] = -objective[j]
	}

	pivot := func(row, col int) {
		pivotVal := tableau[row][col]
		for j := 0; j < width; j++ {
			tableau[row][j] /= pivotVal
		}
		for i := 0; i < height; i++ {
			if i == row {
				continue
			}
			factor := tableau[i][col]
			if math.Abs(factor) <= eps {
				continue
			}
			for j := 0; j < width; j++ {
				tableau[i][j] -= factor * tableau[row][j]
			}
		}
		basic[row] = col
	}

	for {
		enterCol := -1
		minVal := -eps
		for j := 0; j < width-1; j++ {
			if tableau[m][j] < minVal {
				minVal = tableau[m][j]
				enterCol = j
			}
		}
		if enterCol == -1 {
			break
		}

		leaveRow := -1
		bestRatio := 0.0
		for i := 0; i < m; i++ {
			coef := tableau[i][enterCol]
			if coef <= eps {
				continue
			}
			ratio := tableau[i][width-1] / coef
			if leaveRow == -1 || ratio < bestRatio-eps {
				leaveRow = i
				bestRatio = ratio
			}
		}
		if leaveRow == -1 {
			return nil, fmt.Errorf("linear program is unbounded")
		}
		pivot(leaveRow, enterCol)
	}

	solution := make([]float64, n)
	for i := 0; i < m; i++ {
		if basic[i] < n {
			solution[basic[i]] = tableau[i][width-1]
		}
	}
	return solution, nil
}
