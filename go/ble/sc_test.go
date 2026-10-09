// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"encoding/hex"
	"math/big"
	"testing"
)

func h(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// The vectors of checks/ble_models_check.py secure_connections(): Core Vol 3 Part H Appendix D and the pairing
// Apache NimBLE's host tests replay (ble_sm_sc_peer_jw_iio3_rio3_b1_iat0_rat0_ik5_rk7).
func TestSecureConnections(t *testing.T) {
	k := h("2b7e151628aed2a6abf7158809cf4f3c")
	m := h("6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710")
	for _, c := range []struct {
		n    int
		want string
	}{{0, "bb1d6929e95937287fa37d129b756746"}, {16, "070a16b46b4d4144f79bdd9dd04a287c"},
		{40, "dfa66747de9ae63030ca32611497c827"}, {64, "51f0bebf7e3b9d92fc49741779363cfe"}} {
		if got := hex.EncodeToString(AESCMAC(k, m[:c.n])); got != c.want {
			t.Errorf("AES-CMAC of %d octets: %s", c.n, got)
		}
	}
	u := h("e69d350e480103ccdbfdf4ac1191f4efb9a5f9e9a7832c5e2cbe97f2d203b020")
	v := h("fdc57ff449dd4f6bfb7c9df1c29acb592ae7d4eefbfc0a909abbf6323d8b1855")
	x := h("abae2b71ecb2ffff3e7377d15484cbd5")
	y := h("cfc43dfff78365216e5fa725cce7e8a6")
	if got := hex.EncodeToString(F4(u, v, x, 0)); got != "2d8774a9bea1edf11cbda907f116c9f2" {
		t.Errorf("f4: %s", got)
	}
	w := h("98a6bf73f3348d86f166f8b4136b79999b7d390aa610103405adc857a33402ec")
	a1, a2 := Addr7(0, h("cebf37371256")), Addr7(0, h("c1cf2d7013a7"))
	mackey, ltk := F5(w, x, y, a1, a2)
	if hex.EncodeToString(mackey) != "206e63ce206a3ffd024a08a176f16529" || hex.EncodeToString(ltk) != "380a7594b522059823cdd76911798669" {
		t.Errorf("f5: %x %x", mackey, ltk)
	}
	if got := hex.EncodeToString(F6(mackey, x, y, h("c80f2d0cd242da0854bb53b43b34a312"), []byte{1, 1, 2}, a1, a2)); got != "618f95da090b6cd2c5e8d09c9873c4e3" {
		t.Errorf("f6: %s", got)
	}
	if got := G2(u, v, x, y); got != 0x2f9ed5ba%1000000 {
		t.Errorf("g2: %d", got)
	}
	da := LEToBig(h("bd1a3ccda6b8995899b740eb7b60ff4a503f10d2e3b3c974385fc5a3d4f6493f"))
	if got := hex.EncodeToString(PublicKey(da)); got != hex.EncodeToString(u)+"8bd28915d08e1c742430ed8fc24563765c15525abf9a32636deb2a65499c80dc" {
		t.Errorf("debug public key: %s", got)
	}
	db := LEToBig(h("548d20b8970bbc439aad106f6074d46a55c17a178b60e0b45ae658f1ea12d9fb"))
	pka := h("bcf2d8a5dba3956c99f9110d4d2ef0bdee9b69b6cd8874be40e8e5ccdc884453bfa9820e187a14f877fd8e922af85d39d16d921f387499dc6c2c9423f97256ab")
	pkb := PublicKey(db)
	if hex.EncodeToString(pkb) != "728cd188d7be49b2c55c95b364e01232b6c9476337385b9c1e1b1a0609e23185193a296962d630e7e84863dc00730a707d2e29cc917771b175b8f7dcb0e29110" {
		t.Errorf("recorded public key: %x", pkb)
	}
	off := append([]byte(nil), pka...)
	off[32] ^= 1
	if !OnCurve(pka) || OnCurve(off) {
		t.Error("on the curve")
	}
	dh := DHKey(db, pka)
	na, nb := h("a4345fb3af734364cd191b5b87583166"), h("c091fbb377a2020bc6cd6c0451454539")
	ia, ra := Addr7(0, h("ca61a06794e0")), Addr7(0, h("33221100450a"))
	if got := hex.EncodeToString(F4(pkb[:32], pka[:32], nb, 0)); got != "82edd062913d967f13c50d022b5e4316" {
		t.Errorf("recorded confirm: %s", got)
	}
	mackey, ltk = F5(dh, na, nb, ia, ra)
	if hex.EncodeToString(ltk) != "63598a14094b946effae5e538602a36c" {
		t.Errorf("recorded LTK: %x", ltk)
	}
	io := []byte{0x09, 0x00, 0x03}
	if hex.EncodeToString(F6(mackey, na, nb, make([]byte, 16), io, ia, ra)) != "82651d02ed891344041a147c329a1e7d" ||
		hex.EncodeToString(F6(mackey, nb, na, make([]byte, 16), io, ra, ia)) != "063c284ae5484b51654e145e2fddfa22" {
		t.Error("recorded DHKey checks")
	}
	if P256Order.Cmp(big.NewInt(0)) <= 0 {
		t.Error("order")
	}
}
