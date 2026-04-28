package util

import "math/big"

// maxBigInt is a sentinel value larger than any realistic FT balance.
var maxBigInt = new(big.Int).Lsh(big.NewInt(1), 256)

// FindMinFiveSum returns the indices of five elements in balances whose sum is
// the minimum sum >= target. Returns nil if no such combination exists.
// Mirrors TS findMinFiveSum (lib/util/utxoSelect.ts).
func FindMinFiveSum(balances []*big.Int, target *big.Int) []int {
	n := len(balances)
	if n < 5 {
		return nil
	}
	var minFive []int
	minSum := new(big.Int).Set(maxBigInt)
	for i := 0; i <= n-5; i++ {
		for j := i + 1; j <= n-4; j++ {
			left, right := j+1, n-1
			for left < right-1 {
				sum := new(big.Int).Add(balances[i], balances[j])
				sum.Add(sum, balances[left])
				sum.Add(sum, balances[right])
				sum.Add(sum, balances[right-1])
				if sum.Cmp(target) >= 0 && sum.Cmp(minSum) < 0 {
					minSum = new(big.Int).Set(sum)
					minFive = []int{i, j, left, right - 1, right}
				}
				if sum.Cmp(target) < 0 {
					left++
				} else {
					right--
				}
			}
		}
	}
	if len(minFive) != 5 {
		return nil
	}
	return minFive
}
