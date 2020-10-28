package rpc

import (
	"errors"
	"github.com/omnilaboratory/obd/config"
	"github.com/omnilaboratory/obd/tool"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"log"
	"strconv"
	"strings"
	"time"
)

func (client *Client) OmniCreateRawTransaction(fromBitCoinAddress string, toBitCoinAddress string, propertyId int64, amount float64, minerFee float64, sequence int) (retMap map[string]interface{}, err error) {
	beginTime := time.Now()
	log.Println("OmniCreateAndSignRawTransaction beginTime", beginTime.String())
	if tool.CheckIsString(&fromBitCoinAddress) == false {
		return nil, errors.New("fromBitCoinAddress is empty")
	}
	if tool.CheckIsString(&toBitCoinAddress) == false {
		return nil, errors.New("toBitCoinAddress is empty")
	}
	if amount < config.GetOmniDustBtc() {
		return nil, errors.New("wrong amount")
	}

	_, err = client.OmniGetProperty(propertyId)
	if err != nil {
		return nil, err
	}

	pMoney := config.GetOmniDustBtc()
	if minerFee < config.GetOmniDustBtc() {
		minerFee = 0.00003
	}

	balanceResult, err := client.OmniGetbalance(fromBitCoinAddress, int(propertyId))
	if err != nil {
		return nil, err
	}
	omniBalance := gjson.Get(balanceResult, "balance").Float()
	if omniBalance < amount {
		return nil, errors.New("not enough omni balance")
	}

	_, _ = client.ValidateAddress(fromBitCoinAddress)
	resultListUnspent, err := client.ListUnspent(fromBitCoinAddress)
	if err != nil {
		return nil, err
	}

	arrayListUnspent := gjson.Parse(resultListUnspent).Array()
	if len(arrayListUnspent) == 0 {
		return nil, errors.New("empty balance")
	}
	log.Println("listunspent", arrayListUnspent)

	out, _ := decimal.NewFromFloat(minerFee).Add(decimal.NewFromFloat(pMoney)).Round(8).Float64()
	balance := 0.0
	for _, item := range arrayListUnspent {
		balance, _ = decimal.NewFromFloat(balance).Add(decimal.NewFromFloat(item.Get("amount").Float())).Round(8).Float64()
		if balance >= out {
			break
		}
	}

	log.Println("1 balance", balance)
	if balance < out {
		return nil, errors.New("not enough balance")
	}

	//2.Omni_createpayload_simplesend
	payload, err := client.omniCreatePayloadSimpleSend(propertyId, amount)
	if err != nil {
		return nil, err
	}
	log.Println("2 payload " + payload)

	inputs := make([]map[string]interface{}, 0, len(arrayListUnspent))
	for _, item := range arrayListUnspent {
		node := make(map[string]interface{})
		node["txid"] = item.Get("txid").String()
		node["vout"] = item.Get("vout").Int()
		node["confirmations"] = item.Get("confirmations").Int()
		node["spendable"] = item.Get("spendable").Bool()
		node["solvable"] = item.Get("solvable").Bool()
		node["amount"] = item.Get("amount").Float()
		node["address"] = item.Get("address").String()
		node["account"] = item.Get("account").String()
		node["scriptPubKey"] = item.Get("scriptPubKey").String()
		inputs = append(inputs, node)
	}

	outputs := make(map[string]interface{})

	//3.CreateRawTransaction
	createrawtransactionStr, err := client.CreateRawTransaction(inputs, outputs)
	if err != nil {
		return nil, err
	}
	log.Println("3 createrawtransactionStr", createrawtransactionStr)

	//4.Omni_createrawtx_opreturn
	opreturn, err := client.omniCreateRawtxOpreturn(createrawtransactionStr, payload)
	if err != nil {
		return nil, err
	}
	log.Println("4 opreturn", opreturn)

	//5. Omni_createrawtx_reference
	reference, err := client.omniCreateRawtxReference(opreturn, toBitCoinAddress)
	if err != nil {
		return nil, err
	}
	log.Println("5 reference", reference)

	//6.Omni_createrawtx_change
	prevtxs := make([]map[string]interface{}, 0, len(arrayListUnspent))
	for _, item := range arrayListUnspent {
		node := make(map[string]interface{})
		node["txid"] = item.Get("txid").String()
		node["vout"] = item.Get("vout").Int()
		node["scriptPubKey"] = item.Get("scriptPubKey").String()
		node["value"] = item.Get("amount").Float()
		prevtxs = append(prevtxs, node)
	}
	change, err := client.omniCreateRawtxChange(reference, prevtxs, fromBitCoinAddress, minerFee)
	if err != nil {
		return nil, err
	}
	log.Println("6 change", change)

	retMap = make(map[string]interface{})
	retMap["hex"] = change
	retMap["inputs"] = inputs
	return retMap, nil
}

// From channelAddress to temp multi address, to Create CommitmentTx
func (client *Client) OmniCreateRawTransactionUseSingleInput(txType int, fromBitCoinAddress string, toBitCoinAddress string, propertyId int64, amount float64, minerFee float64, sequence int, redeemScript *string, usedTxid string) (retMap map[string]interface{}, currUseTxid string, err error) {
	if tool.CheckIsString(&fromBitCoinAddress) == false {
		return nil, "", errors.New("fromBitCoinAddress is empty")
	}
	if tool.CheckIsString(&toBitCoinAddress) == false {
		return nil, "", errors.New("toBitCoinAddress is empty")
	}
	if amount < config.GetOmniDustBtc() {
		return nil, "", errors.New("wrong amount")
	}
	pMoney := config.GetOmniDustBtc()

	_, _ = client.ValidateAddress(fromBitCoinAddress)
	_, _ = client.ValidateAddress(toBitCoinAddress)
	resultListUnspent, err := client.ListUnspent(fromBitCoinAddress)
	if err != nil {
		return nil, "", err
	}

	arrayListUnspent := gjson.Parse(resultListUnspent).Array()
	//log.Println("listunspent", arrayListUnspent)
	inputCount := 3 + txType
	if len(arrayListUnspent) < inputCount {
		return nil, "", errors.New("wrong input num, need " + strconv.Itoa(inputCount) + " input:one for omni token, " + strconv.Itoa(inputCount-1) + "  btc  inputs for miner fee ")
	}

	balance := 0.0
	inputs := make([]map[string]interface{}, 0, 0)
	currUseTxid = ""
	for _, item := range arrayListUnspent {
		currUseTxid = item.Get("txid").String()
		if usedTxid != "" && strings.Contains(usedTxid, currUseTxid) {
			continue
		}
		inputAmount := item.Get("amount").Float()
		if inputAmount > pMoney {
			node := make(map[string]interface{})
			node["txid"] = item.Get("txid").String()
			node["vout"] = item.Get("vout").Int()
			node["amount"] = inputAmount
			node["scriptPubKey"] = item.Get("scriptPubKey").String()
			if redeemScript != nil {
				node["redeemScript"] = *redeemScript
			}
			balance, _ = decimal.NewFromFloat(balance).Add(decimal.NewFromFloat(node["amount"].(float64))).Round(8).Float64()
			minerFee = GetBtcMinerAmount(balance)
			inputs = append(inputs, node)
			break
		}
	}

	if currUseTxid == "" {
		return nil, "", errors.New("not found the miner fee input")
	}

	minMinerFee := config.GetMinMinerFee(len(inputs))
	if minerFee < minMinerFee {
		minerFee = minMinerFee
	}

	out, _ := decimal.NewFromFloat(pMoney).
		Add(decimal.NewFromFloat(minerFee)).
		Round(8).
		Float64()
	//log.Println("1 balance", balance)
	if balance < out {
		return nil, "", errors.New("not enough balance")
	}
	//2.Omni_createpayload_simplesend
	payload, err := client.omniCreatePayloadSimpleSend(propertyId, amount)
	if err != nil {
		return nil, "", err
	}
	//log.Println("2 payload " + payload)

	outputs := make(map[string]interface{})
	//3.CreateRawTransaction
	createrawtransactionStr, err := client.CreateRawTransaction(inputs, outputs)
	if err != nil {
		return nil, "", err
	}
	//log.Println("3 createrawtransactionStr", createrawtransactionStr)

	//4.Omni_createrawtx_opreturn
	opreturn, err := client.omniCreateRawtxOpreturn(createrawtransactionStr, payload)
	if err != nil {
		return nil, "", err
	}
	//log.Println("4 opreturn", opreturn)

	//5. Omni_createrawtx_reference
	reference, err := client.omniCreateRawtxReference(opreturn, toBitCoinAddress)
	if err != nil {
		return nil, "", err
	}
	//log.Println("5 reference", reference)

	//6.Omni_createrawtx_change
	prevtxs := make([]map[string]interface{}, 0, len(inputs))
	for _, item := range inputs {
		node := make(map[string]interface{})
		node["txid"] = item["txid"]
		node["vout"] = item["vout"]
		node["scriptPubKey"] = item["scriptPubKey"]
		node["value"] = item["amount"]
		if redeemScript != nil {
			node["redeemScript"] = *redeemScript
		}
		prevtxs = append(prevtxs, node)
	}
	change, err := client.omniCreateRawtxChange(reference, prevtxs, toBitCoinAddress, minerFee)
	if err != nil {
		return nil, "", err
	}
	//log.Println("6 change", change)

	retMap = make(map[string]interface{})
	retMap["hex"] = change
	retMap["inputs"] = inputs
	return retMap, currUseTxid, nil
}

// From channelAddress to temp multi address, to Create CommitmentTx
func (client *Client) GetInputInfo(fromBitCoinAddress string, txid, redeemScript string) (info map[string]interface{}, err error) {
	if tool.CheckIsString(&fromBitCoinAddress) == false {
		return nil, errors.New("fromBitCoinAddress is empty")
	}

	_, _ = client.ValidateAddress(fromBitCoinAddress)
	resultListUnspent, err := client.ListUnspent(fromBitCoinAddress)
	if err != nil {
		return nil, err
	}

	arrayListUnspent := gjson.Parse(resultListUnspent).Array()
	for _, item := range arrayListUnspent {
		currUseTxid := item.Get("txid").String()
		if currUseTxid == txid {
			node := make(map[string]interface{})
			node["txid"] = currUseTxid
			node["vout"] = item.Get("vout").Int()
			node["amount"] = item.Get("amount").Float()
			node["scriptPubKey"] = item.Get("scriptPubKey").String()
			if &redeemScript != nil {
				node["redeemScript"] = redeemScript
			}
			return node, nil
		}
	}

	return nil, errors.New("not found input info")
}
