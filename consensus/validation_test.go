package consensus

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"go.sia.tech/core/types"
)

var (
	maxCurrency       = types.NewCurrency(math.MaxUint64, math.MaxUint64)
	testingDifficulty = types.Work{NumHashes: [32]byte{30: 1}}
)

func testingKeypair(seed uint64) (types.PublicKey, ed25519.PrivateKey) {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint64(b, seed)
	privkey := ed25519.NewKeyFromSeed(b)
	var pubkey types.PublicKey
	copy(pubkey[:], privkey[32:])
	return pubkey, privkey
}

func genesisWithBeneficiaries(beneficiaries ...types.Beneficiary) types.Block {
	return types.Block{
		Header:       types.BlockHeader{Timestamp: time.Unix(734600000, 0)},
		Transactions: []types.Transaction{{SiacoinOutputs: beneficiaries}},
	}
}

func signAllInputs(txn *types.Transaction, vc ValidationContext, priv ed25519.PrivateKey) {
	sigHash := vc.SigHash(*txn)
	for i := range txn.SiacoinInputs {
		txn.SiacoinInputs[i].Signatures = []types.InputSignature{types.InputSignature(types.SignHash(priv, sigHash))}
	}
	for i := range txn.SiafundInputs {
		txn.SiafundInputs[i].Signatures = []types.InputSignature{types.InputSignature(types.SignHash(priv, sigHash))}
	}
}

func TestEphemeralOutputs(t *testing.T) {
	pubkey, privkey := testingKeypair(0)
	sau := GenesisUpdate(genesisWithBeneficiaries(types.Beneficiary{
		Address: types.StandardAddress(pubkey),
		Value:   types.Siacoins(1),
	}), testingDifficulty)

	// create an ephemeral output
	parentTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      sau.NewSiacoinOutputs[1],
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   types.Siacoins(1),
		}},
	}
	signAllInputs(&parentTxn, sau.Context, privkey)
	ephemeralOutput := types.SiacoinOutput{
		ID: types.OutputID{
			TransactionID: parentTxn.ID(),
			Index:         0,
		},
		Value:     parentTxn.SiacoinOutputs[0].Value,
		Address:   types.StandardAddress(pubkey),
		LeafIndex: types.EphemeralLeafIndex,
	}

	// create a transaction that spends the ephemeral output
	childTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      ephemeralOutput,
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   ephemeralOutput.Value,
		}},
	}
	signAllInputs(&childTxn, sau.Context, privkey)

	// the transaction set should be valid
	if err := sau.Context.ValidateTransactionSet([]types.Transaction{parentTxn, childTxn}); err != nil {
		t.Fatal(err)
	}

	// change the value of the output and attempt to spend it
	mintTxn := childTxn.DeepCopy()
	mintTxn.SiacoinInputs[0].Parent.Value = types.Siacoins(1e6)
	mintTxn.SiacoinOutputs[0].Value = mintTxn.SiacoinInputs[0].Parent.Value
	signAllInputs(&mintTxn, sau.Context, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{parentTxn, mintTxn}); err == nil {
		t.Fatal("ephemeral output with wrong value should be rejected")
	}

	// add another transaction to the set that double-spends the output
	doubleSpendTxn := childTxn.DeepCopy()
	doubleSpendTxn.SiacoinOutputs[0].Address = types.VoidAddress
	signAllInputs(&doubleSpendTxn, sau.Context, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{parentTxn, childTxn, doubleSpendTxn}); err == nil {
		t.Fatal("ephemeral output double-spend not rejected")
	}

	invalidTxn := childTxn.DeepCopy()
	invalidTxn.SiacoinInputs[0].Parent.Address = types.VoidAddress
	signAllInputs(&invalidTxn, sau.Context, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{parentTxn, invalidTxn}); err == nil {
		t.Fatal("transaction claims wrong address for ephemeral output")
	}
}

func TestValidateTransaction(t *testing.T) {
	// This test constructs a complex transaction and then corrupts it in
	// various ways to produce validation errors. Since the transaction is so
	// complex, we need to perform quite a bit of setup to create the necessary
	// outputs and accumulator state.

	// create genesis block with multiple outputs and file contracts
	pubkey, privkey := testingKeypair(0)
	renterPubkey, renterPrivkey := testingKeypair(1)
	hostPubkey, hostPrivkey := testingKeypair(2)
	data := make([]byte, 64*2)
	rand.Read(data)
	dataRoot := merkleNodeHash(
		storageProofLeafHash(data[:64]),
		storageProofLeafHash(data[64:]),
	)
	genesisBlock := types.Block{
		Header: types.BlockHeader{Timestamp: time.Unix(734600000, 0)},
		Transactions: []types.Transaction{{
			SiacoinOutputs: []types.Beneficiary{
				{
					Address: types.StandardAddress(pubkey),
					Value:   types.Siacoins(11),
				},
				{
					Address: types.StandardAddress(pubkey),
					Value:   types.Siacoins(11),
				},
				{
					Address: types.StandardAddress(pubkey),
					Value:   maxCurrency,
				},
			},
			SiafundOutputs: []types.Beneficiary{
				{
					Address: types.StandardAddress(pubkey),
					Value:   types.Siafunds(100),
				},
				{
					Address: types.StandardAddress(pubkey),
					Value:   types.Siafunds(100),
				},
				{
					Address: types.StandardAddress(pubkey),
					Value:   maxCurrency,
				},
			},
			FileContracts: []types.FileContractState{
				// unresolved open contract
				{
					WindowStart: 5,
					WindowEnd:   10,
					ValidRenterOutput: types.Beneficiary{
						Address: types.StandardAddress(renterPubkey),
						Value:   types.Siacoins(58),
					},
					ValidHostOutput: types.Beneficiary{
						Address: types.StandardAddress(renterPubkey),
						Value:   types.Siacoins(19),
					},
					MissedRenterOutput: types.Beneficiary{
						Address: types.StandardAddress(renterPubkey),
						Value:   types.Siacoins(58),
					},
					MissedHostOutput: types.Beneficiary{
						Address: types.StandardAddress(renterPubkey),
						Value:   types.Siacoins(19),
					},
					RenterPublicKey: renterPubkey,
					HostPublicKey:   hostPubkey,
				},
				// unresolved closed contract
				{
					WindowStart:     0,
					WindowEnd:       10,
					Filesize:        uint64(len(data)),
					FileMerkleRoot:  dataRoot,
					RenterPublicKey: renterPubkey,
					HostPublicKey:   hostPubkey,
				},
				// resolved-valid contract
				{
					WindowStart:     0,
					WindowEnd:       10,
					Filesize:        uint64(len(data)),
					FileMerkleRoot:  dataRoot,
					RenterPublicKey: renterPubkey,
					HostPublicKey:   hostPubkey,
				},
				// resolved-missed contract
				{
					WindowStart:     0,
					WindowEnd:       0,
					RenterPublicKey: renterPubkey,
					HostPublicKey:   hostPubkey,
				},
			},
		}},
	}
	sau := GenesisUpdate(genesisBlock, testingDifficulty)
	spentSC := sau.NewSiacoinOutputs[1]
	unspentSC := sau.NewSiacoinOutputs[2]
	overflowSC := sau.NewSiacoinOutputs[3]
	spentSF := sau.NewSiafundOutputs[0]
	unspentSF := sau.NewSiafundOutputs[1]
	overflowSF := sau.NewSiafundOutputs[2]
	openContract := sau.NewFileContracts[0]
	closedContract := sau.NewFileContracts[1]
	resolvedValidContract := sau.NewFileContracts[2]
	resolvedMissedContract := sau.NewFileContracts[3]
	closedProof := types.StorageProof{
		WindowStart: sau.Context.Index,
		WindowProof: sau.HistoryProof(),
	}
	proofIndex := sau.Context.StorageProofSegmentIndex(closedContract.State.Filesize, closedProof.WindowStart, closedContract.ID)
	copy(closedProof.DataSegment[:], data[64*proofIndex:])
	if proofIndex == 0 {
		closedProof.SegmentProof = append(closedProof.SegmentProof, storageProofLeafHash(data[64:]))
	} else {
		closedProof.SegmentProof = append(closedProof.SegmentProof, storageProofLeafHash(data[:64]))
	}
	resolvedValidProof := types.StorageProof{
		WindowStart: sau.Context.Index,
		WindowProof: sau.HistoryProof(),
	}
	proofIndex = sau.Context.StorageProofSegmentIndex(resolvedValidContract.State.Filesize, resolvedValidProof.WindowStart, resolvedValidContract.ID)
	copy(resolvedValidProof.DataSegment[:], data[64*proofIndex:])
	if proofIndex == 0 {
		resolvedValidProof.SegmentProof = append(resolvedValidProof.SegmentProof, storageProofLeafHash(data[64:]))
	} else {
		resolvedValidProof.SegmentProof = append(resolvedValidProof.SegmentProof, storageProofLeafHash(data[:64]))
	}

	// mine a block so that resolvedMissedContract's proof window expires, then
	// construct a setup transaction that spends some of the outputs and
	// resolves some of the contracts
	b := mineBlock(sau.Context, genesisBlock)
	if err := sau.Context.ValidateBlock(b); err != nil {
		t.Fatal(err)
	}
	sau = ApplyBlock(sau.Context, b)
	sau.UpdateSiacoinOutputProof(&spentSC)
	sau.UpdateSiacoinOutputProof(&unspentSC)
	sau.UpdateSiafundOutputProof(&spentSF)
	sau.UpdateSiafundOutputProof(&unspentSF)
	sau.UpdateFileContractProof(&openContract)
	sau.UpdateFileContractProof(&closedContract)
	sau.UpdateFileContractProof(&resolvedValidContract)
	sau.UpdateFileContractProof(&resolvedMissedContract)
	sau.UpdateWindowProof(&closedProof)
	sau.UpdateWindowProof(&resolvedValidProof)
	resolveTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      spentSC,
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiafundInputs: []types.SiafundInput{{
			Parent:      spentSF,
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.VoidAddress,
			Value:   spentSC.Value,
		}},
		SiafundOutputs: []types.Beneficiary{{
			Address: types.VoidAddress,
			Value:   spentSF.Value,
		}},
		FileContractResolutions: []types.FileContractResolution{
			{
				Parent: resolvedMissedContract,
			},
			{
				Parent:       resolvedValidContract,
				StorageProof: resolvedValidProof,
			},
		},
	}
	signAllInputs(&resolveTxn, sau.Context, privkey)
	b = mineBlock(sau.Context, b, resolveTxn)
	if err := sau.Context.ValidateBlock(b); err != nil {
		t.Fatal(err)
	}
	sau = ApplyBlock(sau.Context, b)
	sau.UpdateSiacoinOutputProof(&spentSC)
	sau.UpdateSiacoinOutputProof(&unspentSC)
	sau.UpdateSiafundOutputProof(&spentSF)
	sau.UpdateSiafundOutputProof(&unspentSF)
	sau.UpdateFileContractProof(&openContract)
	sau.UpdateFileContractProof(&closedContract)
	sau.UpdateFileContractProof(&resolvedValidContract)
	sau.UpdateFileContractProof(&resolvedMissedContract)
	sau.UpdateWindowProof(&closedProof)
	vc := sau.Context

	// finally, create the valid transaction, which spends the remaining outputs
	// and revises/resolves the remaining contracts
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      unspentSC,
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.VoidAddress,
			Value:   types.Siacoins(1),
		}},
		SiafundInputs: []types.SiafundInput{{
			Parent:      unspentSF,
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiafundOutputs: []types.Beneficiary{{
			Address: types.VoidAddress,
			Value:   unspentSF.Value,
		}},
		FileContracts: []types.FileContractState{{
			WindowStart:        100,
			WindowEnd:          105,
			ValidRenterOutput:  types.Beneficiary{Value: types.Siacoins(1)},
			ValidHostOutput:    types.Beneficiary{Value: types.Siacoins(4)},
			MissedRenterOutput: types.Beneficiary{Value: types.Siacoins(2)},
			MissedHostOutput:   types.Beneficiary{Value: types.Siacoins(3)},
		}},
		FileContractRevisions: []types.FileContractRevision{{
			Parent: openContract,
			NewState: types.FileContractState{
				WindowStart:        200,
				WindowEnd:          205,
				ValidRenterOutput:  types.Beneficiary{Value: types.Siacoins(77)},
				ValidHostOutput:    types.Beneficiary{Value: types.Siacoins(0)},
				MissedRenterOutput: types.Beneficiary{Value: types.Siacoins(55)},
				MissedHostOutput:   types.Beneficiary{Value: types.Siacoins(0)},
				RevisionNumber:     1,
			},
		}},
		FileContractResolutions: []types.FileContractResolution{{
			Parent:       closedContract,
			StorageProof: closedProof,
		}},
		MinerFee: types.Siacoins(48).Div64(10),
	}
	signAllInputs(&txn, vc, privkey)
	rev := &txn.FileContractRevisions[0]
	contractHash := vc.ContractSigHash(rev.NewState)
	rev.RenterSignature = types.SignHash(renterPrivkey, contractHash)
	rev.HostSignature = types.SignHash(hostPrivkey, contractHash)

	if err := vc.ValidateTransaction(txn); err != nil {
		t.Fatal(err)
	}

	// corrupt the transaction in various ways to trigger validation errors
	tests := []struct {
		desc    string
		corrupt func(*types.Transaction)
	}{
		{
			"zero-valued SiacoinOutput",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs[0].Value = types.ZeroCurrency
			},
		},
		{
			"zero-valued SiafundOutput",
			func(txn *types.Transaction) {
				txn.SiafundOutputs[0].Value = types.ZeroCurrency
			},
		},
		{
			"siacoin input address does not match spend policy",
			func(txn *types.Transaction) {
				txn.SiacoinInputs[0].SpendPolicy = types.AnyoneCanSpend()
			},
		},
		{
			"siafund input address does not match spend policy",
			func(txn *types.Transaction) {
				txn.SiafundInputs[0].SpendPolicy = types.AnyoneCanSpend()
			},
		},
		{
			"siacoin outputs that do not equal inputs",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs[0].Value = txn.SiacoinOutputs[0].Value.Div64(2)
			},
		},
		{
			"siacoin inputs that overflow",
			func(txn *types.Transaction) {
				txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
					Parent:      overflowSC,
					SpendPolicy: types.PolicyPublicKey(pubkey),
				})
				signAllInputs(txn, vc, privkey)
			},
		},
		{
			"siacoin outputs that overflow",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.Beneficiary{
					Value: maxCurrency,
				})
			},
		},
		{
			"siafund outputs that do not equal inputs",
			func(txn *types.Transaction) {
				txn.SiafundOutputs[0].Value = txn.SiafundOutputs[0].Value.Div64(2)
			},
		},
		{
			"siafund inputs that overflow",
			func(txn *types.Transaction) {
				txn.SiafundInputs = append(txn.SiafundInputs, types.SiafundInput{
					Parent:      overflowSF,
					SpendPolicy: types.PolicyPublicKey(pubkey),
				})
				signAllInputs(txn, vc, privkey)
			},
		},
		{
			"siafund outputs that overflow",
			func(txn *types.Transaction) {
				txn.SiafundOutputs = append(txn.SiafundOutputs, types.Beneficiary{
					Value: maxCurrency,
				})
			},
		},
		{
			"file contract renter output overflows",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.Beneficiary{
					Value: maxCurrency.Sub(types.Siacoins(2)),
				})
				txn.FileContracts[0].ValidRenterOutput.Value = types.Siacoins(2)
			},
		},
		{
			"file contract host output overflows",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.Beneficiary{
					Value: maxCurrency.Sub(types.Siacoins(2)),
				})
				txn.FileContracts[0].ValidRenterOutput.Value = types.ZeroCurrency
				txn.FileContracts[0].ValidHostOutput.Value = types.Siacoins(2)
			},
		},
		{
			"file contract tax overflow",
			func(txn *types.Transaction) {
				txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.Beneficiary{
					Value: maxCurrency.Sub(types.Siacoins(2)),
				})
				txn.FileContracts[0].ValidRenterOutput.Value = types.Siacoins(1)
				txn.FileContracts[0].ValidHostOutput.Value = types.ZeroCurrency
			},
		},
		{
			"miner fee that overflows",
			func(txn *types.Transaction) {
				txn.MinerFee = maxCurrency
			},
		},
		{
			"non-existent siacoin output",
			func(txn *types.Transaction) {
				txn.SiacoinInputs[0].Parent.ID = types.OutputID{}
			},
		},
		{
			"double-spent siacoin output",
			func(txn *types.Transaction) {
				txn.SiacoinInputs[0].Parent = spentSC
			},
		},
		{
			"invalid siacoin signature",
			func(txn *types.Transaction) {
				txn.SiacoinInputs[0].Signatures[0][0] ^= 1
			},
		},
		{
			"non-existent siafund output",
			func(txn *types.Transaction) {
				txn.SiafundInputs[0].Parent.ID = types.OutputID{}
			},
		},
		{
			"double-spent siafund output",
			func(txn *types.Transaction) {
				txn.SiafundInputs[0].Parent = spentSF
			},
		},
		{
			"invalid siafund signature",
			func(txn *types.Transaction) {
				txn.SiafundInputs[0].Signatures[0][0] ^= 1
			},
		},
		{
			"file contract revision that has invalid renter signature",
			func(txn *types.Transaction) {
				txn.FileContractRevisions[0].RenterSignature[0] ^= 1
			},
		},
		{
			"file contract revision that has invalid host signature",
			func(txn *types.Transaction) {
				txn.FileContractRevisions[0].HostSignature[0] ^= 1
			},
		},
		{
			"file contract whose missed payouts exceed its valid payouts",
			func(txn *types.Transaction) {
				txn.FileContracts[0].ValidRenterOutput.Value = types.ZeroCurrency
				txn.MinerFee = types.Siacoins(584).Div64(100)
			},
		},
		{
			"file contract whose window ends before it begins",
			func(txn *types.Transaction) {
				txn.FileContracts[0].WindowEnd = txn.FileContracts[0].WindowStart - 1
			},
		},
		{
			"revision of non-existent file contract",
			func(txn *types.Transaction) {
				txn.FileContractRevisions[0].Parent.ID = types.OutputID{}
			},
		},
		{
			"revision of already-resolved-valid file contract",
			func(txn *types.Transaction) {
				txn.FileContractRevisions[0].Parent = resolvedValidContract
			},
		},
		{
			"revision of already-resolved-missed file contract",
			func(txn *types.Transaction) {
				txn.FileContractRevisions[0].Parent = resolvedMissedContract
			},
		},
		{
			"file contract revision that does not increase revision number",
			func(txn *types.Transaction) {
				rev := &txn.FileContractRevisions[0].NewState
				rev.RevisionNumber = 0
			},
		},
		{
			"file contract revision that modifies valid output sum",
			func(txn *types.Transaction) {
				rev := &txn.FileContractRevisions[0].NewState
				rev.ValidRenterOutput.Value = rev.ValidRenterOutput.Value.Mul64(2)
			},
		},
		{
			"file contract revision whose missed output sum exceeds its valid output sum",
			func(txn *types.Transaction) {
				rev := &txn.FileContractRevisions[0].NewState
				rev.MissedRenterOutput.Value = rev.MissedRenterOutput.Value.Mul64(2)
			},
		},
		{
			"file contract revision whose window ends before it begins",
			func(txn *types.Transaction) {
				rev := &txn.FileContractRevisions[0].NewState
				rev.WindowEnd = rev.WindowStart - 1
			},
		},
		{
			"resolution of non-existent file contract",
			func(txn *types.Transaction) {
				txn.FileContractResolutions[0].Parent.ID = types.OutputID{}
			},
		},
		{
			"resolution with invalid history proof",
			func(txn *types.Transaction) {
				txn.FileContractResolutions[0].StorageProof.WindowProof = nil
			},
		},
		{
			"resolution of already-resolved-valid file contract",
			func(txn *types.Transaction) {
				txn.FileContractResolutions[0].Parent = resolvedValidContract
			},
		},
		{
			"resolution of already-resolved-missed file contract",
			func(txn *types.Transaction) {
				txn.FileContractResolutions[0].Parent = resolvedMissedContract
			},
		},
		{
			"file contract resolution whose WindowStart does not match final revision",
			func(txn *types.Transaction) {
				res := &txn.FileContractResolutions[0]
				res.StorageProof.WindowStart = b.Index()
				res.StorageProof.WindowProof = nil
			},
		},
		{
			"file contract resolution whose storage proof root does not match final Merkle root",
			func(txn *types.Transaction) {
				res := &txn.FileContractResolutions[0]
				res.StorageProof.SegmentProof[0][0] ^= 1
			},
		},
		{
			"invalid Foundation update",
			func(txn *types.Transaction) {
				txn.NewFoundationAddress = types.StandardAddress(pubkey)
			},
		},
	}
	for _, test := range tests {
		corruptTxn := txn.DeepCopy()
		test.corrupt(&corruptTxn)
		if err := vc.ValidateTransaction(corruptTxn); err == nil {
			t.Fatalf("accepted transaction with %v", test.desc)
		}
	}
}

func TestValidateSpendPolicy(t *testing.T) {
	// create a validation context with a height above 0
	vc := ValidationContext{
		Index: types.ChainIndex{Height: 100},
	}

	privkey := func(seed uint64) ed25519.PrivateKey {
		_, privkey := testingKeypair(seed)
		return privkey
	}
	pubkey := func(seed uint64) types.PublicKey {
		pubkey, _ := testingKeypair(seed)
		return pubkey
	}

	tests := []struct {
		desc    string
		policy  types.SpendPolicy
		sign    func(sigHash types.Hash256) []types.InputSignature
		wantErr bool
	}{
		{
			desc: "not enough signatures",
			policy: types.PolicyThreshold{
				N: 2,
				Of: []types.SpendPolicy{
					types.PolicyPublicKey(pubkey(0)),
					types.PolicyPublicKey(pubkey(1)),
				},
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{types.InputSignature(types.SignHash(privkey(0), sigHash))}
			},
			wantErr: true,
		},
		{
			desc:    "height not above",
			policy:  types.PolicyAbove(150),
			sign:    func(types.Hash256) []types.InputSignature { return nil },
			wantErr: true,
		},
		{
			desc:    "anyone can spend",
			policy:  types.AnyoneCanSpend(),
			sign:    func(types.Hash256) []types.InputSignature { return nil },
			wantErr: false,
		},
		{
			desc: "multiple public key signatures",
			policy: types.PolicyThreshold{
				N: 3,
				Of: []types.SpendPolicy{
					types.PolicyPublicKey(pubkey(0)),
					types.PolicyPublicKey(pubkey(1)),
					types.PolicyPublicKey(pubkey(2)),
				},
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(0), sigHash)),
					types.InputSignature(types.SignHash(privkey(1), sigHash)),
					types.InputSignature(types.SignHash(privkey(2), sigHash)),
				}
			},
			wantErr: false,
		},
		{
			desc: "invalid foundation failsafe",
			policy: types.PolicyThreshold{
				N: 1,
				Of: []types.SpendPolicy{
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(0)),
							types.PolicyPublicKey(pubkey(1)),
							types.PolicyPublicKey(pubkey(2)),
						},
					},
					// failsafe policy is not satisfied because the current height is 100
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(3)),
							types.PolicyAbove(150),
						},
					},
				},
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{types.InputSignature(types.SignHash(privkey(3), sigHash))}
			},
			wantErr: true,
		},
		{
			desc: "valid foundation primary",
			policy: types.PolicyThreshold{
				N: 1,
				Of: []types.SpendPolicy{
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(0)),
							types.PolicyPublicKey(pubkey(1)),
							types.PolicyPublicKey(pubkey(2)),
						},
					},
					// failsafe policy is not satisfied because the current height is 100
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(3)),
							types.PolicyAbove(150),
						},
					},
				},
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(1), sigHash)),
					types.InputSignature(types.SignHash(privkey(2), sigHash)),
				}
			},
			wantErr: false,
		},
		{
			desc: "valid foundation failsafe",
			policy: types.PolicyThreshold{
				N: 1,
				Of: []types.SpendPolicy{
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(0)),
							types.PolicyPublicKey(pubkey(1)),
							types.PolicyPublicKey(pubkey(2)),
						},
					},
					// failsafe policy is satisfied because the current height is 100
					types.PolicyThreshold{
						N: 2,
						Of: []types.SpendPolicy{
							types.PolicyPublicKey(pubkey(3)),
							types.PolicyAbove(80),
						},
					},
				},
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{types.InputSignature(types.SignHash(privkey(3), sigHash))}
			},
			wantErr: false,
		},
		{
			desc: "invalid legacy unlock hash",
			policy: types.PolicyUnlockConditions{
				PublicKeys: []types.PublicKey{
					pubkey(0),
					pubkey(1),
					pubkey(2),
				},
				SignaturesRequired: 2,
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(0), sigHash)),
				}
			},
			wantErr: true,
		},
		{
			desc: "invalid timelocked legacy unlock conditions",
			policy: types.PolicyUnlockConditions{
				PublicKeys: []types.PublicKey{
					pubkey(0),
				},
				Timelock:           150,
				SignaturesRequired: 1,
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(0), sigHash)),
				}
			},
			wantErr: true,
		},
		{
			desc: "valid legacy unlock hash",
			policy: types.PolicyUnlockConditions{
				PublicKeys: []types.PublicKey{
					pubkey(0),
					pubkey(1),
					pubkey(2),
				},
				SignaturesRequired: 2,
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(0), sigHash)),
					types.InputSignature(types.SignHash(privkey(1), sigHash)),
				}
			},
			wantErr: false,
		},
		{
			desc: "valid timelocked legacy unlock conditions",
			policy: types.PolicyUnlockConditions{
				PublicKeys: []types.PublicKey{
					pubkey(0),
				},
				Timelock:           80,
				SignaturesRequired: 1,
			},
			sign: func(sigHash types.Hash256) []types.InputSignature {
				return []types.InputSignature{
					types.InputSignature(types.SignHash(privkey(0), sigHash)),
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		txn := types.Transaction{
			SiacoinInputs: []types.SiacoinInput{{
				Parent: types.SiacoinOutput{
					Address: types.PolicyAddress(tt.policy),
				},
				SpendPolicy: tt.policy,
			}},
		}
		sigHash := vc.SigHash(txn)
		txn.SiacoinInputs[0].Signatures = tt.sign(sigHash)
		if err := vc.validSpendPolicies(txn); (err != nil) != tt.wantErr {
			t.Fatalf("case %q failed: %v", tt.desc, err)
		}
	}
}

func TestValidateTransactionSet(t *testing.T) {
	pubkey, privkey := testingKeypair(0)
	genesisBlock := genesisWithBeneficiaries(types.Beneficiary{
		Address: types.StandardAddress(pubkey),
		Value:   types.Siacoins(1),
	})
	// also add some SF
	genesisBlock.Transactions[0].SiafundOutputs = []types.Beneficiary{{
		Address: types.StandardAddress(pubkey),
		Value:   types.Siafunds(100),
	}}
	sau := GenesisUpdate(genesisBlock, testingDifficulty)
	vc := sau.Context

	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      sau.NewSiacoinOutputs[1],
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   sau.NewSiacoinOutputs[1].Value,
		}},
		SiafundInputs: []types.SiafundInput{{
			Parent:      sau.NewSiafundOutputs[0],
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiafundOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   sau.NewSiafundOutputs[0].Value,
		}},
	}
	signAllInputs(&txn, vc, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{txn, txn}); err == nil {
		t.Fatal("accepted transaction set with repeated txn")
	}

	doubleSpendSCTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			Parent:      sau.NewSiacoinOutputs[1],
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiacoinOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   sau.NewSiacoinOutputs[1].Value,
		}},
	}
	signAllInputs(&doubleSpendSCTxn, vc, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{txn, doubleSpendSCTxn}); err == nil {
		t.Fatal("accepted transaction set with double spent siacoin output")
	}

	doubleSpendSFTxn := types.Transaction{
		SiafundInputs: []types.SiafundInput{{
			Parent:      sau.NewSiafundOutputs[0],
			SpendPolicy: types.PolicyPublicKey(pubkey),
		}},
		SiafundOutputs: []types.Beneficiary{{
			Address: types.StandardAddress(pubkey),
			Value:   sau.NewSiafundOutputs[0].Value,
		}},
	}
	signAllInputs(&doubleSpendSFTxn, vc, privkey)

	if err := sau.Context.ValidateTransactionSet([]types.Transaction{txn, doubleSpendSFTxn}); err == nil {
		t.Fatal("accepted transaction set with double spent siafund output")
	}

	// overfill set with copies of txn
	var txns []types.Transaction
	for sau.Context.BlockWeight(txns) < sau.Context.MaxBlockWeight() {
		txns = append(txns, txn)
	}
	if err := sau.Context.ValidateTransactionSet(txns); err == nil {
		t.Fatal("accepted overweight transaction set")
	}
}

func TestValidateBlock(t *testing.T) {
	pubkey, privkey := testingKeypair(0)
	genesis := genesisWithBeneficiaries(types.Beneficiary{
		Address: types.StandardAddress(pubkey),
		Value:   types.Siacoins(1),
	}, types.Beneficiary{
		Address: types.StandardAddress(pubkey),
		Value:   types.Siacoins(1),
	})
	sau := GenesisUpdate(genesis, testingDifficulty)
	vc := sau.Context

	// Mine a block with a few transactions. We are not testing transaction
	// validity here, but the block should still be valid.
	txns := []types.Transaction{
		{
			SiacoinInputs: []types.SiacoinInput{{
				Parent:      sau.NewSiacoinOutputs[1],
				SpendPolicy: types.PolicyPublicKey(pubkey),
			}},
			SiacoinOutputs: []types.Beneficiary{{
				Address: types.VoidAddress,
				Value:   sau.NewSiacoinOutputs[1].Value,
			}},
		},
		{
			SiacoinInputs: []types.SiacoinInput{{
				Parent:      sau.NewSiacoinOutputs[2],
				SpendPolicy: types.PolicyPublicKey(pubkey),
			}},
			MinerFee: sau.NewSiacoinOutputs[2].Value,
		},
	}
	signAllInputs(&txns[0], vc, privkey)
	signAllInputs(&txns[1], vc, privkey)
	b := mineBlock(vc, genesis, txns...)

	tests := []struct {
		desc    string
		corrupt func(*types.Block)
	}{
		{
			"incorrect header block height",
			func(b *types.Block) {
				b.Header.Height = 999
			},
		},
		{
			"incorrect header parent ID",
			func(b *types.Block) {
				b.Header.ParentID[0] ^= 1
			},
		},
		{
			"far-future header timestamp",
			func(b *types.Block) {
				b.Header.Timestamp = time.Now().Round(time.Second).Add(2*time.Hour + time.Minute)
			},
		},
		{
			"long-past header timestamp",
			func(b *types.Block) {
				b.Header.Timestamp = b.Header.Timestamp.Add(-24 * time.Hour)
			},
		},
		{
			"invalid commitment (different miner address)",
			func(b *types.Block) {
				b.Header.MinerAddress[0] ^= 1
			},
		},
		{
			"invalid commitment (different transactions)",
			func(b *types.Block) {
				b.Transactions = b.Transactions[:1]
			},
		},
	}
	for _, test := range tests {
		corruptBlock := b
		test.corrupt(&corruptBlock)
		if err := vc.ValidateBlock(corruptBlock); err == nil {
			t.Fatalf("accepted block with %v", test.desc)
		}
	}
}
