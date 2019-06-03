package db

import (
	"github.com/nknorg/nkn/block"
	. "github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/crypto"
	"github.com/nknorg/nkn/pb"
	"github.com/nknorg/nkn/transaction"
	"github.com/nknorg/nkn/util/config"
	"github.com/nknorg/nnet/log"
)

func (cs *ChainStore) spendTransaction(states *StateDB, txn *transaction.Transaction, totalFee Fixed64, genesis bool) error {
	pl, err := transaction.Unpack(txn.UnsignedTx.Payload)
	if err != nil {
		log.Error("unpack payload error", err)
		return err
	}

	switch txn.UnsignedTx.Payload.Type {
	case pb.CoinbaseType:
		coinbase := pl.(*pb.Coinbase)
		if !genesis {
			donationAmount, err := cs.GetDonation()
			if err != nil {
				log.Error("get donation from store err", err)
				return err
			}
			if err := states.UpdateBalance(BytesToUint160(coinbase.Sender), donationAmount, Subtraction); err != nil {
				return err
			}

		}
		states.IncrNonce(BytesToUint160(coinbase.Sender))
		states.UpdateBalance(BytesToUint160(coinbase.Recipient), Fixed64(coinbase.Amount)+totalFee, Addition)
	case pb.TransferAssetType:
		transfer := pl.(*pb.TransferAsset)
		states.UpdateBalance(BytesToUint160(transfer.Sender), Fixed64(transfer.Amount)+Fixed64(txn.UnsignedTx.Fee), Subtraction)
		states.IncrNonce(BytesToUint160(transfer.Sender))
		states.UpdateBalance(BytesToUint160(transfer.Recipient), Fixed64(transfer.Amount), Addition)

	case pb.RegisterNameType:
		fallthrough
	case pb.DeleteNameType:
		fallthrough
	case pb.SubscribeType:
		pg, err := txn.GetProgramHashes()
		if err != nil {
			return err
		}

		if err := states.UpdateBalance(pg[0], Fixed64(txn.UnsignedTx.Fee), Subtraction); err != nil {
			return err
		}
		states.IncrNonce(pg[0])

	case pb.GenerateIDType:
		genID := pl.(*pb.GenerateID)
		pg, err := txn.GetProgramHashes()
		if err != nil {
			return err
		}

		if err := states.UpdateBalance(pg[0], Fixed64(genID.RegistrationFee)+Fixed64(txn.UnsignedTx.Fee), Subtraction); err != nil {
			return err
		}
		states.IncrNonce(pg[0])
		states.UpdateID(pg[0], crypto.Sha256ZeroHash)

		donationAddress, err := ToScriptHash(config.DonationAddress)
		if err != nil {
			return err
		}
		states.UpdateBalance(donationAddress, Fixed64(genID.RegistrationFee), Addition)

	}

	return nil
}

func (cs *ChainStore) GenerateStateRoot(b *block.Block, needBeCommitted bool) (Uint256, error) {
	_, root, err := cs.generateStateRoot(b, needBeCommitted)

	return root, err
}

func (cs *ChainStore) generateStateRoot(b *block.Block, needBeCommitted bool) (*StateDB, Uint256, error) {
	stateRoot, err := cs.GetCurrentBlockStateRoot()
	if err != nil {
		return nil, EmptyUint256, err
	}
	states, err := NewStateDB(stateRoot, NewTrieStore(cs.GetDatabase()))
	if err != nil {
		return nil, EmptyUint256, err
	}

	//process previous block
	height := b.Header.UnsignedHeader.Height
	if height > config.GenerateIDBlockDelay {
		prevBlock, err := cs.GetBlockByHeight(height - config.GenerateIDBlockDelay)
		if err != nil {
			return nil, EmptyUint256, err
		}

		preBlockHash := prevBlock.Hash()

		for _, txn := range prevBlock.Transactions {
			if txn.UnsignedTx.Payload.Type == pb.GenerateIDType {
				txnHash := txn.Hash()
				data := append(preBlockHash[:], txnHash[:]...)
				data = append(data, b.Header.UnsignedHeader.RandomBeacon...)
				id := crypto.Sha256(data)

				pg, err := txn.GetProgramHashes()
				if err != nil {
					return nil, EmptyUint256, err
				}

				states.UpdateID(pg[0], id)

			}
		}
	}

	var totalFee Fixed64
	for _, txn := range b.Transactions {
		totalFee += Fixed64(txn.UnsignedTx.Fee)
	}

	if height == 0 { //genesisBlock
		cs.spendTransaction(states, b.Transactions[0], totalFee, true)
		for _, txn := range b.Transactions[1:] {
			cs.spendTransaction(states, txn, 0, false)
		}
	} else {
		for _, txn := range b.Transactions {
			cs.spendTransaction(states, txn, totalFee, false)
		}
	}

	var root Uint256
	if needBeCommitted {
		root, err = states.CommitTo(true)
		if err != nil {
			return nil, EmptyUint256, err
		}
	} else {
		root = states.IntermediateRoot(true)
	}

	return states, root, nil
}
