/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package chaincode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	peer2 "github.com/hyperledger-labs/fabric-smart-client/platform/fabric/core/generic/peer"
	"github.com/hyperledger-labs/fabric-smart-client/platform/fabric/core/generic/transaction"
	"github.com/hyperledger-labs/fabric-smart-client/platform/fabric/driver"
	view2 "github.com/hyperledger-labs/fabric-smart-client/platform/view"
	"github.com/hyperledger-labs/fabric-smart-client/platform/view/services/grpc"
	"github.com/hyperledger-labs/fabric-smart-client/platform/view/view"
	pcommon "github.com/hyperledger/fabric-protos-go/common"
	pb "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/pkg/errors"
	"go.uber.org/zap/zapcore"
)

type Invoke struct {
	Chaincode                *Chaincode
	ServiceProvider          view2.ServiceProvider
	Network                  Network
	Channel                  Channel
	TxID                     driver.TxID
	SignerIdentity           view.Identity
	ChaincodePath            string
	ChaincodeName            string
	ChaincodeVersion         string
	TransientMap             map[string][]byte
	Endorsers                []view.Identity
	EndorsersMSPIDs          []string
	ImplicitCollectionMSPIDs []string
	EndorsersFromMyOrg       bool
	EndorsersByConnConfig    []*grpc.ConnectionConfig
	Function                 string
	Args                     []interface{}
}

func NewInvoke(chaincode *Chaincode, function string, args ...interface{}) *Invoke {
	return &Invoke{
		Chaincode:       chaincode,
		ServiceProvider: chaincode.sp,
		Network:         chaincode.network,
		Channel:         chaincode.channel,
		ChaincodeName:   chaincode.name,
		Function:        function,
		Args:            args,
	}
}

func (i *Invoke) Endorse() (driver.Envelope, error) {
	_, prop, responses, signer, err := i.prepare()
	if err != nil {
		return nil, err
	}

	proposalResp := responses[0]
	if proposalResp == nil {
		return nil, errors.New("error during query: received nil proposal response")
	}

	// assemble a signed transaction (it's an Envelope message)
	env, err := CreateSignedTx(prop, signer, responses...)
	if err != nil {
		return nil, errors.WithMessage(err, "could not assemble transaction")
	}

	return transaction.NewEnvelopeFromEnv(env)
}

func (i *Invoke) Query() ([]byte, error) {
	_, _, responses, _, err := i.prepare()
	if err != nil {
		return nil, err
	}
	proposalResp := responses[0]
	if proposalResp == nil {
		return nil, errors.New("error during query: received nil proposal response")
	}
	if proposalResp.Endorsement == nil {
		return nil, errors.Errorf("endorsement failure during query. response: %v", proposalResp.Response)
	}
	return proposalResp.Response.Payload, nil
}

func (i *Invoke) Submit() (string, []byte, error) {
	txid, prop, responses, signer, err := i.prepare()
	if err != nil {
		return "", nil, err
	}

	proposalResp := responses[0]
	if proposalResp == nil {
		return "", nil, errors.New("error during query: received nil proposal response")
	}

	// assemble a signed transaction (it's an Envelope message)
	env, err := CreateSignedTx(prop, signer, responses...)
	if err != nil {
		return txid, proposalResp.Response.Payload, errors.WithMessage(err, "could not assemble transaction")
	}

	// Broadcast envelope and wait for finality
	err = i.broadcast(txid, env)
	if err != nil {
		return "", nil, err
	}

	return txid, proposalResp.Response.Payload, nil
}

func (i *Invoke) WithTransientEntry(k string, v interface{}) driver.ChaincodeInvocation {
	if i.TransientMap == nil {
		i.TransientMap = map[string][]byte{}
	}
	b, err := i.toBytes(v)
	if err != nil {
		panic(err)
	}
	i.TransientMap[k] = b
	return i
}

func (i *Invoke) WithEndorsers(ids ...view.Identity) driver.ChaincodeInvocation {
	i.Endorsers = ids
	return i
}

func (i *Invoke) WithEndorsersByMSPIDs(mspIDs ...string) driver.ChaincodeInvocation {
	i.EndorsersMSPIDs = mspIDs
	return i
}

func (i *Invoke) WithEndorsersFromMyOrg() driver.ChaincodeInvocation {
	i.EndorsersFromMyOrg = true
	return i
}

func (i *Invoke) WithSignerIdentity(id view.Identity) driver.ChaincodeInvocation {
	i.SignerIdentity = id
	return i
}

func (i *Invoke) WithEndorsersByConnConfig(ccs ...*grpc.ConnectionConfig) driver.ChaincodeInvocation {
	i.EndorsersByConnConfig = ccs
	return i
}

func (i *Invoke) WithImplicitCollections(mspIDs ...string) driver.ChaincodeInvocation {
	i.ImplicitCollectionMSPIDs = mspIDs
	return i
}

func (i *Invoke) WithTxID(id driver.TxID) driver.ChaincodeInvocation {
	i.TxID = id
	return i
}

func (i *Invoke) prepare() (string, *pb.Proposal, []*pb.ProposalResponse, driver.SigningIdentity, error) {
	// TODO: improve by providing grpc connection pool
	var peerClients []peer2.Client
	defer func() {
		for _, pCli := range peerClients {
			pCli.Close()
		}
	}()

	if i.SignerIdentity.IsNone() {
		return "", nil, nil, nil, errors.Errorf("no invoker specified")
	}
	if len(i.ChaincodeName) == 0 {
		return "", nil, nil, nil, errors.Errorf("no chaincode specified")
	}

	// load endorser clients
	var endorserClients []pb.EndorserClient
	switch {
	case len(i.EndorsersByConnConfig) != 0:
		for _, config := range i.EndorsersByConnConfig {
			peerClient, err := i.Channel.NewPeerClientForAddress(*config)
			if err != nil {
				return "", nil, nil, nil, err
			}
			peerClients = append(peerClients, peerClient)

			endorserClient, err := peerClient.Endorser()
			if err != nil {
				return "", nil, nil, nil, errors.WithMessagef(err, "error getting endorser client for config %v", config)
			}
			endorserClients = append(endorserClients, endorserClient)
		}
	case len(i.Endorsers) == 0:
		if i.EndorsersFromMyOrg && len(i.EndorsersMSPIDs) == 0 {
			// retrieve invoker's MSP-ID
			invokerMSPID, err := i.Channel.MSPManager().DeserializeIdentity(i.SignerIdentity)
			if err != nil {
				return "", nil, nil, nil, errors.WithMessagef(err, "failed to deserializer the invoker identity")
			}
			i.EndorsersMSPIDs = []string{invokerMSPID.GetMSPIdentifier()}
		}

		// discover
		var err error
		discovery := NewDiscovery(
			i.Chaincode,
		).WithFilterByMSPIDs(
			i.EndorsersMSPIDs...,
		).WithImplicitCollections(i.ImplicitCollectionMSPIDs...)
		i.Endorsers, err = discovery.Call()
		if err != nil {
			return "", nil, nil, nil, err
		}
	case len(i.Endorsers) != 0:
		// nothing to do here
	default:
		return "", nil, nil, nil, errors.New("no rule set to find the endorsers")
	}

	for _, endorser := range i.Endorsers {
		peerClient, err := i.Channel.NewPeerClientForIdentity(endorser)
		if err != nil {
			return "", nil, nil, nil, err
		}
		peerClients = append(peerClients, peerClient)

		endorserClient, err := peerClient.Endorser()
		if err != nil {
			return "", nil, nil, nil, errors.WithMessagef(err, "error getting endorser client for %s", endorser)
		}
		endorserClients = append(endorserClients, endorserClient)
	}
	if len(endorserClients) == 0 {
		return "", nil, nil, nil, errors.New("no endorser clients retrieved - this might indicate a bug")
	}

	// load signer
	signer, err := i.Network.SignerService().GetSigningIdentity(i.SignerIdentity)
	if err != nil {
		return "", nil, nil, nil, err
	}

	// prepare proposal
	signedProp, prop, txid, err := i.prepareProposal(signer)
	if err != nil {
		return "", nil, nil, nil, err
	}

	// collect responses
	responses, err := i.collectResponses(endorserClients, signedProp)
	if err != nil {
		return "", nil, nil, nil, errors.Wrapf(err, "failed collecting proposal responses")
	}

	if len(responses) == 0 {
		// this should only happen if some new code has introduced a bug
		return "", nil, nil, nil, errors.New("no proposal responses received - this might indicate a bug")
	}

	return txid, prop, responses, signer, nil
}

func (i *Invoke) prepareProposal(signer SerializableSigner) (*pb.SignedProposal, *pb.Proposal, string, error) {
	spec, err := i.getChaincodeSpec()
	if err != nil {
		return nil, nil, "", err
	}
	invocation := &pb.ChaincodeInvocationSpec{ChaincodeSpec: spec}

	creator, err := signer.Serialize()
	if err != nil {
		return nil, nil, "", errors.WithMessage(err, "error serializing identity")
	}
	funcName := "invoke"
	prop, txid, err := i.createChaincodeProposalWithTxIDAndTransient(
		pcommon.HeaderType_ENDORSER_TRANSACTION,
		i.Channel.Name(),
		invocation,
		creator,
		i.TransientMap)
	if err != nil {
		return nil, nil, "", errors.WithMessagef(err, "error creating proposal for %s", funcName)
	}
	signedProp, err := protoutil.GetSignedProposal(prop, signer)
	if err != nil {
		return nil, nil, "", errors.WithMessagef(err, "error creating signed proposal for %s", funcName)
	}

	return signedProp, prop, txid, nil
}

// createChaincodeProposalWithTxIDAndTransient creates a proposal from given
// input. It returns the proposal and the transaction id associated with the
// proposal
func (i *Invoke) createChaincodeProposalWithTxIDAndTransient(typ pcommon.HeaderType, channelID string, cis *pb.ChaincodeInvocationSpec, creator []byte, transientMap map[string][]byte) (*pb.Proposal, string, error) {
	var nonce []byte
	var txid string

	if len(i.TxID.Creator) == 0 {
		i.TxID.Creator = creator
	}
	if len(i.TxID.Nonce) == 0 {
		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("generate nonce and tx-id for [%s,%s]", view.Identity(i.TxID.Creator).String(), base64.StdEncoding.EncodeToString(nonce))
		}
		txid = transaction.ComputeTxID(&i.TxID)
		nonce = i.TxID.Nonce
	} else {
		nonce = i.TxID.Nonce
		txid = transaction.ComputeTxID(&i.TxID)
		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("no need to generate nonce and tx-id [%s,%s]", base64.StdEncoding.EncodeToString(nonce), txid)
		}
	}

	if logger.IsEnabledFor(zapcore.DebugLevel) {
		logger.Debugf("create chaincode proposal with tx id [%s], nonce [%s]", txid, base64.StdEncoding.EncodeToString(nonce))
	}

	return protoutil.CreateChaincodeProposalWithTxIDNonceAndTransient(txid, typ, channelID, cis, nonce, creator, transientMap)
}

// collectResponses sends a signed proposal to a set of peers, and gathers all the responses.
func (i *Invoke) collectResponses(endorserClients []pb.EndorserClient, signedProposal *pb.SignedProposal) ([]*pb.ProposalResponse, error) {
	responsesCh := make(chan *pb.ProposalResponse, len(endorserClients))
	errorCh := make(chan error, len(endorserClients))
	wg := sync.WaitGroup{}
	for _, endorser := range endorserClients {
		wg.Add(1)
		go func(endorser pb.EndorserClient) {
			defer wg.Done()
			proposalResp, err := endorser.ProcessProposal(context.Background(), signedProposal)
			if err != nil {
				errorCh <- err
				return
			}
			responsesCh <- proposalResp
		}(endorser)
	}
	wg.Wait()
	close(responsesCh)
	close(errorCh)
	for err := range errorCh {
		return nil, err
	}
	var responses []*pb.ProposalResponse
	for response := range responsesCh {
		responses = append(responses, response)
	}
	return responses, nil
}

// getChaincodeSpec get chaincode spec from the fsccli cmd parameters
func (i *Invoke) getChaincodeSpec() (*pb.ChaincodeSpec, error) {
	// prepare args
	args, err := i.prepareArgs()
	if err != nil {
		return nil, err
	}

	// Build the spec
	input := &pb.ChaincodeInput{
		Args:        args,
		Decorations: nil,
		IsInit:      false,
	}
	chaincodeLang := "golang"
	chaincodeLang = strings.ToUpper(chaincodeLang)
	spec := &pb.ChaincodeSpec{
		Type:        pb.ChaincodeSpec_Type(pb.ChaincodeSpec_Type_value[chaincodeLang]),
		ChaincodeId: &pb.ChaincodeID{Path: i.ChaincodePath, Name: i.ChaincodeName, Version: i.ChaincodeVersion},
		Input:       input,
	}
	return spec, nil
}

func (i *Invoke) prepareArgs() ([][]byte, error) {
	var args [][]byte
	args = append(args, []byte(i.Function))
	for _, arg := range i.Args {
		b, err := i.toBytes(arg)
		if err != nil {
			return nil, err
		}
		args = append(args, b)
	}
	return args, nil
}

func (i *Invoke) toBytes(arg interface{}) ([]byte, error) {
	switch v := arg.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	case int:
		return []byte(strconv.Itoa(v)), nil
	case int64:
		return []byte(strconv.FormatInt(v, 10)), nil
	case uint64:
		return []byte(strconv.FormatUint(v, 10)), nil
	default:
		return nil, errors.Errorf("arg type [%T] not recognized.", v)
	}
}

func (i *Invoke) broadcast(txID string, env *pcommon.Envelope) error {
	if err := i.Network.Broadcast(env); err != nil {
		return err
	}
	return i.Channel.IsFinal(txID)
}

func CreateSignedTx(
	proposal *pb.Proposal,
	signer protoutil.Signer,
	resps ...*pb.ProposalResponse,
) (*pcommon.Envelope, error) {
	if len(resps) == 0 {
		return nil, errors.New("at least one proposal response is required")
	}

	// the original header
	hdr, err := protoutil.UnmarshalHeader(proposal.Header)
	if err != nil {
		return nil, err
	}

	// the original payload
	pPayl, err := protoutil.UnmarshalChaincodeProposalPayload(proposal.Payload)
	if err != nil {
		return nil, err
	}

	// check that the signer is the same that is referenced in the header
	// TODO: maybe worth removing?
	signerBytes, err := signer.Serialize()
	if err != nil {
		return nil, err
	}

	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(signerBytes, shdr.Creator) {
		return nil, errors.New("signer must be the same as the one referenced in the header")
	}

	// ensure that all actions are bitwise equal and that they are successful
	var a1 []byte
	var first *pb.ProposalResponse
	for n, r := range resps {
		if r.Response.Status < 200 || r.Response.Status >= 400 {
			return nil, errors.Errorf("proposal response was not successful, error code %d, msg %s", r.Response.Status, r.Response.Message)
		}

		if n == 0 {
			a1 = r.Payload
			first = r
			continue
		}

		if !bytes.Equal(a1, r.Payload) {
			upr1, err := UnpackProposalResponse(first.Payload)
			if err != nil {
				return nil, err
			}
			rwset1, err := json.MarshalIndent(upr1.TxRwSet, "", "  ")
			if err != nil {
				return nil, err
			}

			upr2, err := UnpackProposalResponse(r.Payload)
			if err != nil {
				return nil, err
			}
			rwset2, err := json.MarshalIndent(upr2.TxRwSet, "", "  ")
			if err != nil {
				return nil, err
			}

			if !bytes.Equal(rwset1, rwset2) {
				if logger.IsEnabledFor(zapcore.DebugLevel) {
					logger.Debugf("ProposalResponsePayloads do not match (%v) \n[%s]\n!=\n[%s]",
						bytes.Equal(rwset1, rwset2), string(rwset1), string(rwset2),
					)
				}
			} else {
				pr1, err := json.MarshalIndent(first, "", "  ")
				if err != nil {
					return nil, err
				}
				pr2, err := json.MarshalIndent(r, "", "  ")
				if err != nil {
					return nil, err
				}

				if logger.IsEnabledFor(zapcore.DebugLevel) {
					logger.Debugf("ProposalResponse do not match  \n[%s]\n!=\n[%s]",
						bytes.Equal(pr1, pr2), string(pr1), string(pr2),
					)
				}
			}

			return nil, errors.Errorf(
				"ProposalResponsePayloads do not match [%s]!=[%s]",
				base64.StdEncoding.EncodeToString(a1),
				base64.StdEncoding.EncodeToString(r.Payload),
			)
		}
	}

	// fill endorsements
	endorsements := make([]*pb.Endorsement, len(resps))
	for n, r := range resps {
		endorsements[n] = r.Endorsement
	}

	// create ChaincodeEndorsedAction
	cea := &pb.ChaincodeEndorsedAction{ProposalResponsePayload: resps[0].Payload, Endorsements: endorsements}

	// obtain the bytes of the proposal payload that will go to the transaction
	propPayloadBytes, err := protoutil.GetBytesProposalPayloadForTx(pPayl)
	if err != nil {
		return nil, err
	}

	// serialize the chaincode action payload
	cap := &pb.ChaincodeActionPayload{ChaincodeProposalPayload: propPayloadBytes, Action: cea}
	capBytes, err := protoutil.GetBytesChaincodeActionPayload(cap)
	if err != nil {
		return nil, err
	}

	// create a transaction
	taa := &pb.TransactionAction{Header: hdr.SignatureHeader, Payload: capBytes}
	taas := make([]*pb.TransactionAction, 1)
	taas[0] = taa
	tx := &pb.Transaction{Actions: taas}

	// serialize the tx
	txBytes, err := protoutil.GetBytesTransaction(tx)
	if err != nil {
		return nil, err
	}

	// create the payload
	payl := &pcommon.Payload{Header: hdr, Data: txBytes}
	paylBytes, err := protoutil.GetBytesPayload(payl)
	if err != nil {
		return nil, err
	}

	// sign the payload
	sig, err := signer.Sign(paylBytes)
	if err != nil {
		return nil, err
	}

	// here's the envelope
	return &pcommon.Envelope{Payload: paylBytes, Signature: sig}, nil
}
