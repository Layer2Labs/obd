package lightclient

import (
	"encoding/json"
	"errors"
	"log"
	"obd/bean"
	"obd/bean/enum"
	"obd/rpc"
	"obd/service"
	"obd/tool"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func (client *Client) Write() {
	defer func() {
		e := client.Socket.Close()
		if e != nil {
			log.Println(e)
		} else {
			log.Println("socket closed after writing...")
		}
	}()

	for {
		select {
		case data, ok := <-client.SendChannel:
			if !ok {
				_ = client.Socket.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			log.Println("send data", string(data))
			_ = client.Socket.WriteMessage(websocket.TextMessage, data)
		}
	}
}

func (client *Client) Read() {
	defer func() {
		_ = service.UserService.UserLogout(client.User)
		if client.User != nil {
			delete(GlobalWsClientManager.OnlineUserMap, client.User.PeerId)
			delete(service.OnlineUserMap, client.User.PeerId)
			client.User = nil
		}
		GlobalWsClientManager.Disconnected <- client
		_ = client.Socket.Close()
		log.Println("socket closed after reading...")
	}()

	for {
		_, dataReq, err := client.Socket.ReadMessage()
		if err != nil {
			log.Println(err)
			break
		}

		var msg bean.RequestMessage
		log.Println("request data: ", string(dataReq))
		parse := gjson.Parse(string(dataReq))

		if parse.Exists() == false {
			log.Println("wrong json input")
			client.sendToMyself(enum.MsgType_Error_0, false, string(dataReq))
			continue
		}

		msg.Type = enum.MsgType(parse.Get("type").Int())
		msg.Data = parse.Get("data").String()
		msg.RawData = string(dataReq)
		msg.SenderUserPeerId = parse.Get("sender_user_peer_id").String()
		msg.RecipientUserPeerId = parse.Get("recipient_user_peer_id").String()
		msg.RecipientNodePeerId = parse.Get("recipient_node_peer_id").String()
		msg.PubKey = parse.Get("pub_key").String()
		msg.Signature = parse.Get("signature").String()

		// check the Recipient is online
		if tool.CheckIsString(&msg.RecipientUserPeerId) {
			_, err := client.FindUser(&msg.RecipientUserPeerId)
			if err != nil {
				if tool.CheckIsString(&msg.RecipientNodePeerId) == false {
					client.sendToMyself(msg.Type, false, "can not find target user")
					continue
				}
			}
		}

		// check the data whether is right signature
		if tool.CheckIsString(&msg.PubKey) && tool.CheckIsString(&msg.Signature) {
			rpcClient := rpc.NewClient()
			result, err := rpcClient.VerifyMessage(msg.PubKey, msg.Signature, msg.Data)
			if err != nil {
				client.sendToMyself(msg.Type, false, err.Error())
				continue
			}
			if gjson.Parse(result).Bool() == false {
				client.sendToMyself(msg.Type, false, "error signature")
				continue
			}
		}

		var sendType = enum.SendTargetType_SendToNone
		status := false
		var dataOut []byte
		var needLogin = true
		if msg.Type < 1000 && msg.Type >= 0 {
			if msg.Type == enum.MsgType_GetMnemonic_101 {
				sendType, dataOut, status = client.hdWalletModule(msg)
			} else {
				sendType, dataOut, status = client.userModule(msg)
			}
			needLogin = false
		}

		if msg.Type > 1000 {
			sendType, dataOut, status = client.omniCoreModule(msg)
			needLogin = false
		}

		if needLogin {
			//not login
			if client.User == nil {
				client.sendToMyself(msg.Type, false, "please login")
				continue
			} else { // already login

				if msg.Type == enum.MsgType_ChannelOpen_N32 || msg.Type == enum.MsgType_ChannelAccept_N33 ||
					msg.Type == enum.MsgType_FundingCreate_BtcFundingCreated_N3400 || msg.Type == enum.MsgType_FundingSign_BtcSign_N3500 ||
					msg.Type == enum.MsgType_FundingCreate_AssetFundingCreated_N34 || msg.Type == enum.MsgType_FundingSign_AssetFundingSigned_N35 ||
					msg.Type == enum.MsgType_CommitmentTxSigned_RevokeAndAcknowledgeCommitmentTransaction_N352 ||
					msg.Type == enum.MsgType_CommitmentTx_CommitmentTransactionCreated_N351 ||
					msg.Type == enum.MsgType_CloseChannelRequest_N38 || msg.Type == enum.MsgType_CloseChannelSign_N39 ||
					msg.Type == enum.MsgType_HTLC_AddHTLC_N40 || msg.Type == enum.MsgType_HTLC_AddHTLCSigned_N41 ||
					msg.Type == enum.MsgType_HTLC_SendR_N46 || msg.Type == enum.MsgType_HTLC_VerifyR_N47 ||
					msg.Type == enum.MsgType_HTLC_RequestCloseCurrTx_N48 || msg.Type == enum.MsgType_HTLC_CloseSigned_N49 ||
					msg.Type == enum.MsgType_Atomic_Swap_N80 || msg.Type == enum.MsgType_Atomic_Swap_Accept_N81 {
					if tool.CheckIsString(&msg.RecipientUserPeerId) == false {
						client.sendToMyself(msg.Type, false, "error recipient_user_peer_id")
						continue
					}
					if tool.CheckIsString(&msg.RecipientNodePeerId) == false {
						client.sendToMyself(msg.Type, false, "error recipient_node_peer_id")
						continue
					}
					if p2pChannelMap[msg.RecipientNodePeerId] == nil {
						client.sendToMyself(msg.Type, false, "not connect recipient_node_peer_id")
						continue
					}
					if msg.RecipientNodePeerId == P2PLocalPeerId {
						if _, err := FindUserOnLine(&msg.RecipientUserPeerId); err != nil {
							client.sendToMyself(msg.Type, false, err.Error())
							continue
						}
					}
				}

				msg.SenderUserPeerId = client.User.PeerId
				for {
					typeStr := strconv.Itoa(int(msg.Type))
					//-200 -201
					if msg.Type <= enum.MsgType_Mnemonic_CreateAddress_N200 && msg.Type >= enum.MsgType_Mnemonic_GetAddressByIndex_201 {
						sendType, dataOut, status = client.hdWalletModule(msg)
						break
					}

					//-32 -3201 -3202 -3203 -3204
					if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_ChannelOpen_N32))) {
						sendType, dataOut, status = client.channelModule(msg)
						break
					}
					//-33 -3301 -3302 -3303 -3304
					if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_ChannelAccept_N33))) {
						sendType, dataOut, status = client.channelModule(msg)
						break
					}
					//-34 -3400 -3401 -3402 -3403 -3404
					if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_FundingCreate_AssetFundingCreated_N34))) {
						sendType, dataOut, status = client.fundingTransactionModule(msg)
						break
					}

					//-35 -3500
					if msg.Type == enum.MsgType_FundingSign_AssetFundingSigned_N35 ||
						msg.Type == enum.MsgType_FundingSign_BtcSign_N3500 {
						sendType, dataOut, status = client.fundingSignModule(msg)
						break
					}

					if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_FundingSign_AssetFundingSigned_N35))) {
						//-351 -35101 -35102 -35103 -35104
						if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_CommitmentTx_CommitmentTransactionCreated_N351))) {
							sendType, dataOut, status = client.commitmentTxModule(msg)
							break
						}
						//-352 -35201 -35202 -35203 -35204
						if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_CommitmentTxSigned_RevokeAndAcknowledgeCommitmentTransaction_N352))) {
							sendType, dataOut, status = client.commitmentTxSignModule(msg)
							break
						}
						//-353 -35301 -35302 -35303 -35304
						if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_CommitmentTxSigned_ToAliceSign_N353))) {
							sendType, dataOut, status = client.otherModule(msg)
							break
						}
						//-354 -35401 -35402 -35403 -35404
						if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_CommitmentTxSigned_SecondToBobSign_N354))) {
							sendType, dataOut, status = client.otherModule(msg)
							break
						}
					}

					//-38
					if msg.Type == enum.MsgType_CloseChannelRequest_N38 ||
						msg.Type == enum.MsgType_CloseChannelSign_N39 {
						sendType, dataOut, status = client.channelModule(msg)
						break
					}

					//-40 -41
					if strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_HTLC_AddHTLC_N40))) ||
						strings.HasPrefix(typeStr, strconv.Itoa(int(enum.MsgType_HTLC_AddHTLCSigned_N41))) {
						sendType, dataOut, status = client.htlcHDealModule(msg)
						break
					}

					//-42 -43 -44 -45 -46 -47
					if msg.Type <= enum.MsgType_HTLC_PayerSignC3b_N42 && msg.Type >= enum.MsgType_HTLC_VerifyR_N47 {
						sendType, dataOut, status = client.htlcTxModule(msg)
						break
					}

					// -48 -49
					if msg.Type == enum.MsgType_HTLC_RequestCloseCurrTx_N48 ||
						msg.Type == enum.MsgType_HTLC_CloseSigned_N49 {
						sendType, dataOut, status = client.htlcCloseModule(msg)
						break
					}
					// -80 -81
					if msg.Type == enum.MsgType_Atomic_Swap_Accept_N81 ||
						msg.Type == enum.MsgType_Atomic_Swap_N80 {
						sendType, dataOut, status = client.atomicSwapModule(msg)
						break
					}
					break
				}
			}
		}

		if len(dataOut) == 0 {
			dataOut = dataReq
		}

		//broadcast except me
		if sendType == enum.SendTargetType_SendToExceptMe {
			for itemClient := range GlobalWsClientManager.ClientsMap {
				if itemClient != client {
					jsonMessage := getReplyObj(string(dataOut), msg.Type, status, client, itemClient)
					itemClient.SendChannel <- jsonMessage
				}
			}
		}
		//broadcast to all
		if sendType == enum.SendTargetType_SendToAll {
			jsonMessage := getReplyObj(string(dataOut), msg.Type, status, client, nil)
			GlobalWsClientManager.Broadcast <- jsonMessage
		}
	}
}

func getReplyObj(data string, msgType enum.MsgType, status bool, fromClient, toClient *Client) []byte {
	var jsonMessage []byte

	fromId := fromClient.Id
	if fromClient.User != nil {
		fromId = fromClient.User.PeerId
	}

	toClientId := "all"
	if toClient != nil {
		toClientId = toClient.Id
		if toClient.User != nil {
			toClientId = toClient.User.PeerId
		}
	}

	if strings.Contains(fromId, "@/") == false {
		fromId = fromId + "@" + localServerDest
	}
	node := make(map[string]interface{})
	err := json.Unmarshal([]byte(data), &node)
	if err == nil {
		parse := gjson.Parse(data)
		jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: parse.Value()})
	} else {
		if strings.Contains(err.Error(), " array into Go value of type map") {
			parse := gjson.Parse(data)
			jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: parse.Value()})
		} else {
			jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: data})
		}
	}
	return jsonMessage
}
func getP2PReplyObj(data string, msgType enum.MsgType, status bool, fromId, toClientId string) []byte {
	var jsonMessage []byte
	node := make(map[string]interface{})
	err := json.Unmarshal([]byte(data), &node)
	if err == nil {
		parse := gjson.Parse(data)
		jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: parse.Value()})
	} else {
		if strings.Contains(err.Error(), " array into Go value of type map") {
			parse := gjson.Parse(data)
			jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: parse.Value()})
		} else {
			jsonMessage, _ = json.Marshal(&bean.ReplyMessage{Type: msgType, Status: status, From: fromId, To: toClientId, Result: data})
		}
	}
	return jsonMessage
}

func (client *Client) sendToMyself(msgType enum.MsgType, status bool, data string) {
	jsonMessage := getReplyObj(data, msgType, status, client, client)
	client.SendChannel <- jsonMessage
}

func (client *Client) sendToSomeone(msgType enum.MsgType, status bool, recipientPeerId string, data string) error {
	if tool.CheckIsString(&recipientPeerId) {
		if _, err := client.FindUser(&recipientPeerId); err == nil {
			itemClient := GlobalWsClientManager.OnlineUserMap[recipientPeerId]
			if itemClient != nil && itemClient.User != nil {
				jsonMessage := getReplyObj(data, msgType, status, client, itemClient)
				itemClient.SendChannel <- jsonMessage
				return nil
			}
		}
	}
	return errors.New("recipient not exist or online")
}

//发送消息给对方，分为同节点和不同节点的两种情况
func (client *Client) sendDataToP2PUser(msg bean.RequestMessage, status bool, data string) error {
	msg.SenderUserPeerId = client.User.PeerId
	msg.SenderNodePeerId = client.User.P2PLocalPeerId
	if tool.CheckIsString(&msg.RecipientUserPeerId) && tool.CheckIsString(&msg.RecipientNodePeerId) {
		//如果是同一个obd节点
		if msg.RecipientNodePeerId == P2PLocalPeerId {
			if _, err := FindUserOnLine(&msg.RecipientUserPeerId); err == nil {
				itemClient := GlobalWsClientManager.OnlineUserMap[msg.RecipientUserPeerId]
				if itemClient != nil && itemClient.User != nil {
					//因为数据库，分库，需要对特定的消息进行处理
					if status {
						//收到请求后，首先对消息进行处理
						retData, err := routerOfP2PNode(msg.Type, data, itemClient)
						if err != nil {
							return err
						} else {
							if tool.CheckIsString(&retData) {
								data = retData
							}
						}

						//需要节点之间本身的通信 bob的节点响应352后，发送353到alice节点，353处理完成后，需要对353的结果消息进行分发
						//当前的353消息本身是从bob发给Alice的
						if msg.Type == enum.MsgType_CommitmentTxSigned_ToAliceSign_N353 {
							//	发给bob的信息
							newMsg := bean.RequestMessage{}
							newMsg.Type = enum.MsgType_CommitmentTxSigned_SecondToBobSign_N354
							newMsg.SenderUserPeerId = itemClient.User.PeerId
							newMsg.SenderNodePeerId = P2PLocalPeerId
							newMsg.RecipientUserPeerId = msg.SenderUserPeerId
							newMsg.RecipientNodePeerId = msg.SenderNodePeerId
							newMsg.Data = data
							//转发给bob，
							_ = itemClient.sendDataToP2PUser(newMsg, true, data)

						}

						//当354处理完成，就改成352的返回 353和354对用户是透明的
						if msg.Type == enum.MsgType_CommitmentTxSigned_SecondToBobSign_N354 {
							return nil
						}

						if msg.Type == enum.MsgType_HTLC_PayerSignC3b_N42 {
							jsonObj := gjson.Parse(data)
							approval := jsonObj.Get("approval").Bool()
							if approval {
								newMsg := bean.RequestMessage{}
								newMsg.Type = enum.MsgType_HTLC_PayeeCreateHTRD1a_N43
								newMsg.SenderUserPeerId = itemClient.User.PeerId
								newMsg.SenderNodePeerId = P2PLocalPeerId
								newMsg.RecipientUserPeerId = msg.SenderUserPeerId
								newMsg.RecipientNodePeerId = msg.SenderNodePeerId
								newMsg.Data = data
								//转发给bob
								_ = itemClient.sendDataToP2PUser(newMsg, true, data)
								return nil
							} else {
								msg.Type = enum.MsgType_HTLC_AddHTLCSigned_N41
							}
						}

						//当43处理完成，就改成41的返回 42和43对用户是透明的
						if msg.Type == enum.MsgType_HTLC_PayeeCreateHTRD1a_N43 {
							newMsg := bean.RequestMessage{}
							newMsg.Type = enum.MsgType_HTLC_PayerSignHTRD1a_N44
							newMsg.SenderUserPeerId = itemClient.User.PeerId
							newMsg.SenderNodePeerId = P2PLocalPeerId
							newMsg.RecipientUserPeerId = msg.SenderUserPeerId
							newMsg.RecipientNodePeerId = msg.SenderNodePeerId
							newMsg.Data = data
							//转发给payer alice，
							_ = itemClient.sendDataToP2PUser(newMsg, true, data)
							return nil
						}

						//当43处理完成，就改成41的返回 42和43对用户是透明的
						if msg.Type == enum.MsgType_HTLC_PayerSignHTRD1a_N44 {
							msg.Type = enum.MsgType_HTLC_AddHTLCSigned_N41
						}
					}
					fromId := msg.SenderUserPeerId + "@" + p2pChannelMap[msg.SenderNodePeerId].Address
					toId := msg.RecipientNodePeerId + "@" + p2pChannelMap[msg.RecipientNodePeerId].Address
					jsonMessage := getP2PReplyObj(data, msg.Type, status, fromId, toId)
					itemClient.SendChannel <- jsonMessage
					return nil
				}
			}
		} else { //不通的p2p的节点 需要转发到对方的节点
			msgToOther := bean.RequestMessage{}
			msgToOther.Type = msg.Type
			msgToOther.SenderNodePeerId = P2PLocalPeerId
			msgToOther.SenderUserPeerId = msg.SenderUserPeerId
			msgToOther.RecipientUserPeerId = msg.RecipientUserPeerId
			msgToOther.RecipientNodePeerId = msg.RecipientNodePeerId
			msgToOther.Data = data
			bytes, err := json.Marshal(msgToOther)
			if err == nil {
				return SendP2PMsg(msg.RecipientNodePeerId, string(bytes))
			}
		}
	}
	return errors.New("recipient not exist or online")
}

//当p2p收到消息后
func getDataFromP2PSomeone(msg bean.RequestMessage) error {
	if tool.CheckIsString(&msg.RecipientUserPeerId) && tool.CheckIsString(&msg.RecipientNodePeerId) {
		if msg.RecipientNodePeerId == P2PLocalPeerId {
			if _, err := FindUserOnLine(&msg.RecipientUserPeerId); err == nil {
				itemClient := GlobalWsClientManager.OnlineUserMap[msg.RecipientUserPeerId]
				if itemClient != nil && itemClient.User != nil {
					//收到数据后，需要对其进行加工
					retData, err := routerOfP2PNode(msg.Type, msg.Data, itemClient)
					if err != nil {
						return err
					} else {
						if tool.CheckIsString(&retData) {
							msg.Data = retData
						}
					}

					//需要节点之间本身的通信 bob的节点响应352后，发送353到alice节点，353处理完成后，需要对353的结果消息进行分发
					//当前的353消息本身是从bob发给Alice的
					if msg.Type == enum.MsgType_CommitmentTxSigned_ToAliceSign_N353 {
						//	发给bob的信息
						newMsg := bean.RequestMessage{}
						newMsg.Type = enum.MsgType_CommitmentTxSigned_SecondToBobSign_N354
						newMsg.SenderNodePeerId = itemClient.User.PeerId
						newMsg.SenderNodePeerId = P2PLocalPeerId
						newMsg.RecipientUserPeerId = msg.SenderUserPeerId
						newMsg.RecipientNodePeerId = msg.SenderNodePeerId
						newMsg.Data = msg.Data
						_ = itemClient.sendDataToP2PUser(newMsg, true, msg.Data)
						return nil
					}

					//当354处理完成，就改成352的返回 353和354对用户是透明的
					if msg.Type == enum.MsgType_CommitmentTxSigned_SecondToBobSign_N354 {
						return nil
					}

					fromId := msg.SenderUserPeerId + "@" + p2pChannelMap[msg.SenderNodePeerId].Address
					toId := msg.RecipientUserPeerId + "@" + p2pChannelMap[msg.RecipientNodePeerId].Address
					jsonMessage := getP2PReplyObj(msg.Data, msg.Type, true, fromId, toId)
					itemClient.SendChannel <- jsonMessage
					return nil
				}
			}
		}
	}
	return errors.New("recipient not exist or online")
}

func (client *Client) FindUser(peerId *string) (*Client, error) {
	if tool.CheckIsString(peerId) {
		itemClient := GlobalWsClientManager.OnlineUserMap[*peerId]
		if itemClient != nil && itemClient.User != nil {
			return itemClient, nil
		}
	}
	return nil, errors.New("user not exist or online")
}
func FindUserOnLine(peerId *string) (*Client, error) {
	if tool.CheckIsString(peerId) {
		itemClient := GlobalWsClientManager.OnlineUserMap[*peerId]
		if itemClient != nil && itemClient.User != nil {
			return itemClient, nil
		}
	}
	return nil, errors.New(*peerId + " not exist or online")
}
