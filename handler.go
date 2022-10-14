package main

import (
	"github.com/CuteReimu/goutil/slices"
	"github.com/CuteReimu/goutil/strings"
	"github.com/Touhou-Freshman-Camp/bingo-server/myws"
	"github.com/dgraph-io/badger/v3"
	"github.com/pkg/errors"
	"github.com/spf13/cast"
	"time"
)

var handlers = map[string]func(player *PlayerConn, protoName string, result map[string]interface{}) error{
	"login_cs":            handleLogin,
	"heart_cs":            handleHeart,
	"create_room_cs":      handleCreateRoom,
	"join_room_cs":        handleJoinRoom,
	"leave_room_cs":       handleLeaveRoom,
	"update_room_type_cs": handleUpdateRoomType,
	"update_name_cs":      handleUpdateName,
	"start_game_cs":       handleStartGame,
	"get_spells_cs":       handleGetSpells,
	"stop_game_cs":        handleStopGame,
	"update_spell_cs":     handleUpdateSpell,
}

func handleUpdateSpell(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	idx, err := cast.ToUint32E(data["idx"])
	if err != nil {
		return errors.WithStack(err)
	}
	if idx >= 25 {
		return errors.New("idx超出范围")
	}
	statusVal, err := cast.ToInt32E(data["status"])
	if err != nil {
		return errors.WithStack(err)
	}
	if _, ok := SpellStatus_name[statusVal]; !ok {
		return errors.New("status不合法")
	}
	status := SpellStatus(statusVal)
	if status == SpellStatus_banned || status == SpellStatus_both_select {
		return errors.New("status不合法")
	}
	var newStatus SpellStatus
	var tokens []string
	now := time.Now().UnixMilli()
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if err != nil {
			return err
		}
		if !room.Started {
			return errors.New("游戏还没开始")
		}
		if room.StartMs <= now-int64(room.GameTime)*60000-int64(room.Countdown)*1000 {
			return errors.New("游戏时间到")
		}
		st := room.Status[idx]
		if room.StartMs > now-int64(room.Countdown)*1000 && !status.isSelectStatus() && !(status == SpellStatus_none && st.isSelectStatus()) {
			return errors.New("倒计时还没结束")
		}
		tokens = append(tokens, room.Host)
		switch playerConn.token {
		case room.Host:
			if status == SpellStatus_none && st.isSelectStatus() || status.isSelectStatus() {
				return errors.New("权限不足")
			}
			newStatus = status
			tokens = append(tokens, room.Players...)
		case room.Players[0]:
			if status.isRightStatus() || st == SpellStatus_left_get && status != SpellStatus_left_get {
				return errors.New("权限不足")
			}
			if st == SpellStatus_right_get {
				return errors.New("对方已经打完")
			}
			switch status {
			case SpellStatus_left_get:
				newStatus = status
			case SpellStatus_left_select:
				if st == SpellStatus_right_select {
					newStatus = SpellStatus_both_select
				} else {
					newStatus = status
				}
			case SpellStatus_none:
				if st == SpellStatus_both_select {
					newStatus = SpellStatus_right_select
				} else {
					newStatus = status
				}
			}
			tokens = append(tokens, room.Players[0])
			if status != SpellStatus_left_select && status != SpellStatus_none {
				tokens = append(tokens, room.Players[1])
			}
		case room.Players[1]:
			if status.isLeftStatus() || st == SpellStatus_right_get && status != SpellStatus_right_get {
				return errors.New("权限不足")
			}
			if st == SpellStatus_left_get {
				return errors.New("对方已经打完")
			}
			switch status {
			case SpellStatus_right_get:
				newStatus = status
			case SpellStatus_right_select:
				if st == SpellStatus_left_select {
					newStatus = SpellStatus_both_select
				} else {
					newStatus = status
				}
			case SpellStatus_none:
				if st == SpellStatus_both_select {
					newStatus = SpellStatus_left_select
				} else {
					newStatus = status
				}
			}
			tokens = append(tokens, room.Players[1])
			if status != SpellStatus_right_select && status != SpellStatus_none {
				tokens = append(tokens, room.Players[0])
			}
		}
		room.Status[idx] = newStatus
		return SetRoom(txn, room)
	})
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if len(token) > 0 {
			if conn, ok := tokenConnMap[token]; ok {
				message := &myws.Message{
					MsgName: "update_spell_sc",
					Data: map[string]interface{}{
						"idx":    idx,
						"status": int32(newStatus),
					},
				}
				if token == playerConn.token {
					message.Reply = protoName
				}
				conn.Send(message)
			}
		}
	}
	return nil
}

func handleStopGame(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	var winnerIdx int32 = -1
	var err error
	if winner, ok := data["winner"]; ok {
		winnerIdx, err = cast.ToInt32E(winner)
		if err != nil {
			return errors.WithStack(err)
		}
		if winnerIdx != 0 && winnerIdx != 1 {
			return errors.New("winner不正确")
		}
	}
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if err != nil {
			return err
		}
		if room.Host != playerConn.token {
			return errors.New("你不是房主")
		} else if !room.Started {
			return errors.New("游戏还没开始")
		}
		if winnerIdx >= 0 {
			room.Score[winnerIdx]++
		}
		room.Started = false
		room.Spells = nil
		room.StartMs = 0
		room.GameTime = 0
		room.Countdown = 0
		room.Status = nil
		return SetRoom(txn, room)
	})
	if err != nil {
		return err
	}
	if winnerIdx == -1 {
		playerConn.NotifyPlayerInfo(protoName)
	} else {
		playerConn.NotifyPlayerInfo(protoName, KVPair{"winner", winnerIdx})
	}
	return nil
}

func handleGetSpells(playerConn *PlayerConn, protoName string, _ map[string]interface{}) error {
	var spells []*Spell
	var startTime int64
	var gameTime, countdown uint32
	var status []int32
	err := db.View(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if err != nil {
			return err
		}
		if !room.Started {
			return errors.New("游戏还未开始")
		}
		spells = room.Spells
		startTime = room.StartMs
		countdown = room.Countdown
		gameTime = room.GameTime
		if playerConn.token == room.Players[0] {
			status = slices.Map(room.Status, func(e SpellStatus) int32 { return int32(e.hideRightSelect()) })
		} else if playerConn.token == room.Players[1] {
			status = slices.Map(room.Status, func(e SpellStatus) int32 { return int32(e.hideLeftSelect()) })
		} else {
			status = slices.Map(room.Status, func(e SpellStatus) int32 { return int32(e) })
		}
		return nil
	})
	if err != nil {
		return err
	}
	message := &myws.Message{
		MsgName: "spell_list_sc",
		Reply:   protoName,
		Data: map[string]interface{}{
			"spells":     spells,
			"time":       time.Now().UnixMilli(),
			"start_time": startTime,
			"game_time":  gameTime,
			"countdown":  countdown,
		},
	}
	if len(status) > 0 {
		message.Data["status"] = status
	}
	playerConn.Send(message)
	return nil
}

func handleStartGame(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	gameTime, err := cast.ToUint32E(data["game_time"])
	if err != nil {
		return errors.WithStack(err)
	}
	if gameTime == 0 {
		return errors.New("游戏时间不能为0")
	}
	if gameTime > 1440 {
		return errors.New("游戏时间太长")
	}
	countdown, err := cast.ToUint32E(data["countdown"])
	if err != nil {
		return errors.WithStack(err)
	}
	if countdown > 86400 {
		return errors.New("倒计时太长")
	}
	games, err := cast.ToStringSliceE(data["games"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(games) > 99 {
		return errors.New("选择的作品数太多")
	}
	startTime := time.Now().UnixMilli()
	var spells []*Spell
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if err != nil {
			return err
		}
		if room.Host != playerConn.token {
			return errors.New("你不是房主")
		} else if room.Started {
			return errors.New("游戏已经开始")
		} else if slices.Any(room.Players, strings.IsEmpty) {
			return errors.New("玩家没满")
		}
		spells, err = RandSpells(games)
		if err != nil {
			return errors.Wrap(err, "随符卡失败")
		}
		room.Started = true
		room.Spells = spells
		room.StartMs = startTime
		room.Countdown = countdown
		room.GameTime = gameTime
		room.Status = make([]SpellStatus, len(spells))
		return SetRoom(txn, room)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayersInRoom(protoName, &myws.Message{
		MsgName: "spell_list_sc",
		Data: map[string]interface{}{
			"spells":     spells,
			"time":       startTime,
			"start_time": startTime,
			"game_time":  gameTime,
			"countdown":  countdown,
		},
	})
	return nil
}

func handleUpdateName(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	name, err := cast.ToStringE(data["name"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(name) == 0 {
		return errors.New("名字为空")
	}
	if len(name) > 48 {
		return errors.New("名字太长")
	}
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		player.Name = name
		return SetPlayer(txn, player)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayerInfo(protoName)
	return nil
}

func handleUpdateRoomType(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	roomType, err := cast.ToInt32E(data["type"])
	if err != nil {
		return errors.WithStack(err)
	}
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if IsErrKeyNotFound(err) {
			return errors.New("不在房间里")
		} else if err != nil {
			return err
		}
		if room.Host != playerConn.token {
			return errors.New("不是房主")
		}
		room.RoomType = roomType
		return SetRoom(txn, room)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayerInfo(protoName)
	return nil
}

func handleLeaveRoom(playerConn *PlayerConn, protoName string, _ map[string]interface{}) error {
	var tokens []string
	var roomDestroyed bool
	err := db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) == 0 {
			return errors.New("并不在房间里")
		}
		room, err := GetRoom(txn, player.RoomId)
		if IsErrKeyNotFound(err) {
			return errors.New("不在房间里")
		} else if err != nil {
			return err
		}
		if room.GetStarted() {
			return errors.New("比赛已经开始了，不能退出")
		}
		player.RoomId = ""
		player.Name = ""
		if room.Host == player.Token {
			for i := range room.Players {
				if len(room.Players[i]) != 0 && room.Players[i] != room.Host {
					tokens = append(tokens, room.Players[i])
					p, err := GetPlayer(txn, room.Players[i])
					if err != nil {
						return err
					}
					p.RoomId = ""
					p.Name = ""
					err = SetPlayer(txn, p)
					if err != nil {
						return err
					}
				}
			}
			if err = DelRoom(txn, room.RoomId); err != nil {
				return err
			}
			roomDestroyed = true
		} else {
			for i := range room.Players {
				if room.Players[i] == player.Token {
					room.Players[i] = ""
				} else {
					tokens = append(tokens, room.Players[i])
				}
			}
			err = SetRoom(txn, room)
			if err != nil {
				return err
			}
		}
		return SetPlayer(txn, player)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayerInfo(protoName)
	for _, token := range tokens {
		conn := tokenConnMap[token]
		if conn != nil {
			if roomDestroyed {
				conn.Send(&myws.Message{MsgName: "room_info_sc"})
			} else {
				conn.NotifyPlayerInfo("")
				break
			}
		}
	}
	return nil
}

func handleJoinRoom(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	name, err := cast.ToStringE(data["name"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(name) == 0 {
		return errors.New("名字为空")
	}
	if len(name) > 48 {
		return errors.New("名字太长")
	}
	rid, err := cast.ToStringE(data["rid"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(rid) == 0 {
		return errors.New("房间ID为空")
	}
	if len(rid) > 16 {
		return errors.New("房间ID太长")
	}
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) != 0 {
			_, err = GetRoom(txn, player.RoomId)
			if err == nil {
				return errors.New("已经在房间里了")
			} else if !IsErrKeyNotFound(err) {
				return errors.WithStack(err)
			}
		}
		room, err := GetRoom(txn, rid)
		if IsErrKeyNotFound(err) {
			return errors.New("房间不存在")
		} else if err != nil {
			return err
		}
		host, err := GetPlayer(txn, room.Host)
		if err != nil {
			return err
		}
		if host.Name == name {
			return errors.New("名字重复")
		}
		player.RoomId = rid
		player.Name = name
		if err = SetPlayer(txn, player); err != nil {
			return err
		}
		var ok bool
		for i := range room.Players {
			if ok {
				if len(room.Players[i]) != 0 {
					player2, err := GetPlayer(txn, room.Players[i])
					if err != nil {
						return err
					}
					if player2.Name == name {
						return errors.New("名字重复")
					}
				}
			} else if len(room.Players[i]) == 0 {
				ok = true
				room.Players[i] = player.Token
			}
		}
		if !ok {
			return errors.New("房间满了")
		}
		return SetRoom(txn, room)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayerInfo(protoName)
	return nil
}

func handleCreateRoom(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	name, err := cast.ToStringE(data["name"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(name) == 0 {
		return errors.New("名字为空")
	}
	if len(name) > 48 {
		return errors.New("名字太长")
	}
	rid, err := cast.ToStringE(data["rid"])
	if err != nil {
		return errors.WithStack(err)
	}
	if len(rid) == 0 {
		return errors.New("房间ID为空")
	}
	if len(rid) > 16 {
		return errors.New("房间ID太长")
	}
	roomType, err := cast.ToInt32E(data["type"])
	if err != nil {
		return errors.WithStack(err)
	}
	err = db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayer(txn, playerConn.token)
		if err != nil {
			return err
		}
		if len(player.RoomId) != 0 {
			_, err = GetRoom(txn, player.RoomId)
			if err == nil {
				return errors.New("已经在房间里了")
			} else if !IsErrKeyNotFound(err) {
				return errors.WithStack(err)
			}
		}
		key := append([]byte("room: "), []byte(rid)...)
		_, err = txn.Get(key)
		if err == nil {
			return errors.New("房间已存在")
		} else if err != badger.ErrKeyNotFound {
			return errors.WithStack(err)
		}
		var room = Room{
			RoomId:   rid,
			RoomType: roomType,
			Host:     playerConn.token,
			Players:  make([]string, 2),
			Score:    make([]uint32, 2),
		}
		player.RoomId = rid
		player.Name = name
		if err = SetPlayer(txn, player); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		return SetRoom(txn, &room)
	})
	if err != nil {
		return err
	}
	playerConn.NotifyPlayerInfo(protoName)
	return nil
}

func handleHeart(playerConn *PlayerConn, protoName string, _ map[string]interface{}) error {
	playerConn.Send(&myws.Message{
		MsgName: "heart_sc",
		Reply:   protoName,
		Data: map[string]interface{}{
			"time": time.Now().UnixMilli(),
		},
	})
	return nil
}

func handleLogin(playerConn *PlayerConn, protoName string, data map[string]interface{}) error {
	token, ok := data["token"]
	if !ok {
		playerConn.SendError(protoName, 400, "no token")
		return nil
	}
	tokenStr, _ := token.(string)
	if len(tokenStr) == 0 || len(tokenStr) > 128 || !isAlphaNum(tokenStr) {
		playerConn.SendError(protoName, 400, "invalid token")
		return nil
	}
	err := db.Update(func(txn *badger.Txn) error {
		player, err := GetPlayerOrNew(txn, tokenStr)
		if err != nil {
			return err
		}
		player.Token = tokenStr
		if _, ok := tokenConnMap[tokenStr]; ok {
			return errors.New("already online")
		} else {
			tokenConnMap[tokenStr] = playerConn
		}
		return SetPlayer(txn, player)
	})
	if err != nil {
		return err
	}
	playerConn.token = tokenStr
	message, _, err := playerConn.buildPlayerInfo()
	message.Reply = protoName
	if err != nil {
		return err
	}
	playerConn.Send(message)
	return nil
}
