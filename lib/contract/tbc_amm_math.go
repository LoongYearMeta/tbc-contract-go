package contract

import (
	"math/big"
)

const TBCAMMRetainedPoolSat uint64 = 1500
const TBCAMMMaxAmount uint64 = 1<<63 - 1

type TBCAMMMathState struct{ LP, FT, TBC, Value uint64 }
type TBCAMMFees struct{ Total, LP, ServiceAccrued, ServicePaid, Net uint64 }
type TBCAMMQuote struct {
	Next                                   TBCAMMMathState
	LPDelta, FTDelta, TBCDelta, ValueDelta uint64
	Fees                                   *TBCAMMFees
}

func TBCAMMFeePolicy(plan uint8) (uint16, uint16, string, error) {
	addresses := []string{"", "13oCEJaqyyiC8iRrfup6PDL2GKZ3xQrsZL", "1Fa6Uy64Ub4qNdB896zX2pNMx4a8zMhtCy", "125fTLNsraQxTYqT4EeQNF2ggzcqicveKL", "19DetoaaohQkjFVJ6oGXd83xhZYQSbpE1g", "15EKrhuD8Yf3SfhjAgbizYqfnBbKh9ZMZ7", "1N7rf2AuAHB2aCrVgnbQhSWhaUVk3rGhjm"}
	if plan < 1 || plan > 6 {
		return 0, 0, "", modernError("LP plan must be 1..6")
	}
	return []uint16{0, 35, 35, 135, 335, 535, 330}[plan], []uint16{0, 25, 5, 5, 5, 5, 200}[plan], addresses[plan], nil
}
func ammAmount(n uint64) error {
	if n > TBCAMMMaxAmount {
		return modernError("amount exceeds signed 63-bit range")
	}
	return nil
}
func ammPositive(n uint64) error {
	if n == 0 {
		return modernError("amount must be positive")
	}
	return ammAmount(n)
}
func ammRatio(a, b, d uint64, ceil bool) (uint64, error) {
	if d == 0 {
		return 0, modernError("zero denominator")
	}
	n := new(big.Int).Mul(new(big.Int).SetUint64(a), new(big.Int).SetUint64(b))
	den := new(big.Int).SetUint64(d)
	if ceil {
		n.Add(n, new(big.Int).Sub(den, big.NewInt(1)))
	}
	n.Quo(n, den)
	if !n.IsUint64() || n.Uint64() > TBCAMMMaxAmount {
		return 0, modernError("ratio overflow")
	}
	return n.Uint64(), nil
}
func (s TBCAMMMathState) Validate(active bool) error {
	for _, n := range []uint64{s.LP, s.FT, s.TBC, s.Value} {
		if e := ammAmount(n); e != nil {
			return e
		}
	}
	if s.Value < 1500 || active && (s.LP == 0 || s.FT == 0 || s.TBC == 0) {
		return modernError("invalid Pool math state")
	}
	return nil
}
func CalculateTBCAMMFees(base uint64, plan uint8) (*TBCAMMFees, error) {
	if e := ammAmount(base); e != nil {
		return nil, e
	}
	total, lp, _, e := TBCAMMFeePolicy(plan)
	if e != nil {
		return nil, e
	}
	t, _ := ammRatio(base, uint64(total), 10000, false)
	l, _ := ammRatio(base, uint64(lp), 10000, false)
	a := t - l
	paid := a
	if a < 10 {
		paid = 0
	}
	return &TBCAMMFees{Total: t, LP: l, ServiceAccrued: a, ServicePaid: paid, Net: base - t}, nil
}
func (s TBCAMMMathState) Add(budget uint64, useTBC bool, firstFT *uint64) (*TBCAMMQuote, error) {
	if e := s.Validate(false); e != nil {
		return nil, e
	}
	if e := ammPositive(budget); e != nil {
		return nil, e
	}
	var lp, ft, tbc, value uint64
	var e error
	if s.LP == 0 && s.FT == 0 && s.TBC == 0 {
		if s.Value != 1500 || !useTBC || firstFT == nil {
			return nil, modernError("first liquidity requires TBC budget and initial FT")
		}
		if e = ammPositive(*firstFT); e != nil {
			return nil, e
		}
		lp = budget
		ft = *firstFT
		tbc = budget
		value = budget
	} else {
		if e = s.Validate(true); e != nil {
			return nil, e
		}
		if firstFT != nil {
			return nil, modernError("initial FT only for empty pool")
		}
		redeem := s.Value - 1500
		if redeem < s.TBC {
			return nil, modernError("actual reserve below pricing reserve")
		}
		den := s.FT
		if useTBC {
			den = redeem
		}
		lp, e = ammRatio(budget, s.LP, den, false)
		if e != nil {
			return nil, e
		}
		if e = ammPositive(lp); e != nil {
			return nil, e
		}
		ft, e = ammRatio(s.FT, lp, s.LP, true)
		if e != nil {
			return nil, e
		}
		tbc, e = ammRatio(s.TBC, lp, s.LP, true)
		if e != nil {
			return nil, e
		}
		value, e = ammRatio(redeem, lp, s.LP, true)
		if e != nil {
			return nil, e
		}
	}
	next := TBCAMMMathState{s.LP + lp, s.FT + ft, s.TBC + tbc, s.Value + value}
	if e = next.Validate(true); e != nil {
		return nil, e
	}
	return &TBCAMMQuote{Next: next, LPDelta: lp, FTDelta: ft, TBCDelta: tbc, ValueDelta: value}, nil
}
func (s TBCAMMMathState) Remove(burn uint64) (*TBCAMMQuote, error) {
	if e := s.Validate(false); e != nil {
		return nil, e
	}
	if e := ammPositive(burn); e != nil {
		return nil, e
	}
	if burn > s.LP {
		return nil, modernError("burn exceeds LP supply")
	}
	ft, _ := ammRatio(s.FT, burn, s.LP, false)
	tbc, _ := ammRatio(s.TBC, burn, s.LP, false)
	value, _ := ammRatio(s.Value-1500, burn, s.LP, false)
	if value < 10 {
		return nil, modernError("payout below 10 sat")
	}
	next := TBCAMMMathState{s.LP - burn, s.FT - ft, s.TBC - tbc, s.Value - value}
	return &TBCAMMQuote{Next: next, LPDelta: burn, FTDelta: ft, TBCDelta: tbc, ValueDelta: value}, next.Validate(false)
}
func (s TBCAMMMathState) SwapFT(input uint64, plan uint8, min uint64) (*TBCAMMQuote, error) {
	if e := s.Validate(true); e != nil {
		return nil, e
	}
	if e := ammPositive(input); e != nil {
		return nil, e
	}
	if e := ammAmount(min); e != nil {
		return nil, e
	}
	fees, e := CalculateTBCAMMFees(input, plan)
	if e != nil {
		return nil, e
	}
	inc := fees.Net
	value := input - fees.ServicePaid
	if inc == 0 || inc >= s.TBC || value <= inc {
		return nil, modernError("swap requires bounded input and retained fee")
	}
	tbc := s.TBC + inc
	out, e := ammRatio(s.FT, inc, tbc, false)
	if e != nil {
		return nil, e
	}
	if out == 0 || out < min {
		return nil, modernError("FT payout below minimum")
	}
	next := TBCAMMMathState{s.LP, s.FT - out, tbc, s.Value + value}
	if e = next.Validate(true); e != nil {
		return nil, e
	}
	return &TBCAMMQuote{Next: next, FTDelta: out, TBCDelta: inc, ValueDelta: value, Fees: fees}, nil
}
func (s TBCAMMMathState) SwapTBC(input uint64, plan uint8, min uint64) (*TBCAMMQuote, error) {
	if e := s.Validate(true); e != nil {
		return nil, e
	}
	if e := ammPositive(input); e != nil {
		return nil, e
	}
	if e := ammAmount(min); e != nil {
		return nil, e
	}
	if input >= s.FT {
		return nil, modernError("FT increment must be below reserve")
	}
	ft := s.FT + input
	gross, e := ammRatio(s.TBC, input, ft, false)
	if e != nil {
		return nil, e
	}
	fees, e := CalculateTBCAMMFees(gross, plan)
	if e != nil {
		return nil, e
	}
	value := fees.Net + fees.ServicePaid
	if fees.Net < 10 || fees.Net < min || value == 0 || value >= gross || value > s.Value {
		return nil, modernError("TBC payout or retained fee outside range")
	}
	next := TBCAMMMathState{s.LP, ft, s.TBC - gross, s.Value - value}
	if e = next.Validate(true); e != nil {
		return nil, e
	}
	return &TBCAMMQuote{Next: next, FTDelta: input, TBCDelta: gross, ValueDelta: value, Fees: fees}, nil
}
