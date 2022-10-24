package msim

import (
	"crypto/sha1"
	"encoding/base64"
	"phantom/util"
	"strconv"
	"strings"
	"time"
)

/*
	msim_not_a_packet   = -2       	-> garbage
	msim_unknown_packet = -1       	-> unknown packet
	msim_error          = iota - 2 	-> error

	msim_login_initial  			-> lc 1
	msim_login_response 			-> lc 2
	msim_keepalive      			-> keepalive
	msim_callback_reply 			-> persistr

	msim_login_challenge  			-> login2
	msim_logout           			-> logout
	msim_callback_request 			-> persist
*/

// login
func handleClientAuthentication(client *Msim_client) bool {
	util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
		msim_new_data_string("lc", "1"),
		msim_new_data_string("nc", base64.StdEncoding.EncodeToString([]byte(client.Nonce))),
		msim_new_data_string("id", "1"),
	}))

	loginpacket, success := util.ReadTraffic(client.Connection)
	if !success {
		util.Error("Failed to read Login2 Data Packet!")
		return false
	}

	username := findValueFromKey("username", loginpacket)
	version := findValueFromKey("clientver", loginpacket)

	acc := getUserData(username)
	client.Account = acc
	uid := acc.Uid
	sessionkey := GenerateSessionKey()
	screenname := acc.Screenname
	password := acc.Password
	client.Sessionkey = sessionkey
	byte_nc2 := make([]byte, 32)
	byte_rc4_key := make([]byte, 16)
	byte_challenge := []byte(client.Nonce)
	for i := 0; i < 32; i++ {
		byte_nc2[i] = byte_challenge[i+32]
	}
	byte_password := util.ConvertToUtf16(password)
	hasher := sha1.New()
	hasher.Write(byte_password)
	byte_hash_phase1 := hasher.Sum(nil)

	byte_hash_phase2 := append(byte_hash_phase1, byte_nc2...)
	hasher.Reset()
	hasher.Write(byte_hash_phase2)
	byte_hash_total := hasher.Sum(nil)
	hasher.Reset()

	for i := 0; i < 16; i++ {
		byte_rc4_key[i] = byte_hash_total[i]
	}
	packetrc4data := findValueFromKey("response", loginpacket)
	byte_rc4_data, err := base64.StdEncoding.DecodeString(packetrc4data)
	if err != nil {
		util.Error("Invalid base64 provided at login packet.")
		return false
	}
	rc4data := util.DecryptRC4(byte_rc4_key, byte_rc4_data)

	if strings.Contains(string(rc4data), username) {
		util.Log("MySpaceIM", "Client Authenticated! | Username: %s | Screenname: %s | Version: 1.0.%s.0", username, screenname, version)
		util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
			msim_new_data_string("lc", "2"),
			msim_new_data_int("sesskey", sessionkey),
			msim_new_data_int("proof", sessionkey),
			msim_new_data_int("userid", uid),
			msim_new_data_int("profileid", uid),
			msim_new_data_string("uniquenick", screenname),
			msim_new_data_string("id", "1"),
		}))

		return true
	} else {
		util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
			msim_new_data_boolean("error", true),
			msim_new_data_string("errmsg", "The password provided is incorrect."),
			msim_new_data_string("err", "260"),
			msim_new_data_boolean("fatal", true),
		}))
	}
	return false
}

// Status Messages
func handleClientPacketSetStatusMessages(client *Msim_client, packet []byte) {
	status := findValueFromKey("status", packet)
	statstring := findValueFromKey("statstring", packet)

	client.StatusCode = status
	client.StatusText = statstring

	for i := 0; i < len(Clients); i++ {
		if Clients[i].Account.Uid != client.Account.Uid {
			util.WriteTraffic(Clients[i].Connection, buildDataPacket([]msim_data_pair{
				msim_new_data_int("bm", 100),
				msim_new_data_int("f", client.Account.Uid),
				msim_new_data_string("msg", "|s|"+status+"|ss|"+statstring+""),
			}))
		}
	}
}

// addbuddy message
func handleClientPacketAddBuddy(client *Msim_client, packet []byte) {
	if findValueFromKey("newprofileid", packet) == "6221" {
		util.Debug("dummy packet detected")
		return
	}
	newprofileid := findValueFromKey("newprofileid", packet)
	reason := findValueFromKey("reason", packet)

	var count int
	check, _ := util.GetDatabaseHandle().Query("SELECT COUNT(*) from contacts WHERE id=?", newprofileid)
	check.Next()
	check.Scan(&count)
	check.Close()
	if count > 0 {
		util.Debug("buddy is already added")
		util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
			msim_new_data_boolean("error", true),
			msim_new_data_string("errmsg", "The profile requested is already a buddy."),
			msim_new_data_int("err", 1539),
		}))
		return
	}
	dbres, _ := util.GetDatabaseHandle().Query("INSERT into contacts (`fromid`, `id`, `reason`) VALUES (?, ?, ?)", client.Account.Uid, newprofileid, reason)
	dbres.Close()
}

// delbuddy message
func handleClientPacketDelBuddy(client *Msim_client, packet []byte) {
	delprofileid := findValueFromKey("delprofileid", packet)
	dbres, _ := util.GetDatabaseHandle().Query("DELETE from contacts WHERE id=?", delprofileid)
	dbres.Close()
}

func handleClientOfflineEvents(client *Msim_client) {
	for i := 0; i < len(Clients); i++ {
		if Clients[i].Account.Uid != client.Account.Uid {
			util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
				msim_new_data_int("bm", 100),
				msim_new_data_int("f", Clients[i].Account.Uid),
				msim_new_data_string("msg", "|s|"+Clients[i].StatusCode+"|ss|"+Clients[i].StatusText),
			}))
		}
	}

	//offline messages
	res, _ := util.GetDatabaseHandle().Query("SELECT * from offlinemessages WHERE toid= ?", client.Account.Uid)
	for res.Next() {
		var msg OfflineMessage
		_ = res.Scan(&msg.fromid, &msg.toid, &msg.date, &msg.msg)
		util.WriteTraffic(client.Connection, buildDataPacket([]msim_data_pair{
			msim_new_data_int("bm", 1),
			msim_new_data_int("sesskey", client.Sessionkey),
			msim_new_data_int("f", msg.fromid),
			msim_new_data_int64("date", msg.date),
			msim_new_data_string("msg", msg.msg),
		}))
		util.Debug("%d", msg.date)
	}
	res.Close()
	res2, _ := util.GetDatabaseHandle().Query("DELETE from offlinemessages WHERE toid= ?", client.Account.Uid)
	res2.Close()
}

// bm type 1
func handleClientPacketBuddyInstantMessage(client *Msim_client, packet []byte) {
	t, _ := strconv.Atoi(findValueFromKey("t", packet))
	msg := findValueFromKey("msg", packet)
	date := time.Now().UTC().UnixNano()
	found := false
	for i := 0; i < len(Clients); i++ {
		if Clients[i].Account.Uid == t {
			found = true
			util.WriteTraffic(Clients[i].Connection, buildDataPacket([]msim_data_pair{
				msim_new_data_int("bm", 1),
				msim_new_data_int("sesskey", Clients[i].Sessionkey),
				msim_new_data_int("f", client.Account.Uid),
				msim_new_data_string("msg", msg),
			}))
		}
	}
	if !found {
		if !strings.Contains(msg, "%typing%") && !strings.Contains(msg, "%stoptyping%") {
			res, _ := util.GetDatabaseHandle().Query("INSERT INTO offlinemessages (`fromid`, `toid`, `date`, `msg`) VALUES (?, ?, ?, ?)", client.Account.Uid, t, date, msg)
			res.Close()
		}
	}
}

// persist 1;0;1 get_contact_information
func handleClientPacketGetContactList(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)
	util.Debug("get_contact_list")
	res, _ := util.GetDatabaseHandle().Query("SELECT * from contacts WHERE fromid=?", client.Account.Uid)
	body := ""
	for res.Next() {
		var contact Contact
		_ = res.Scan(&contact.fromid, &contact.id, &contact.reason)
		accountRow := getUserDataById(contact.id)
		body += buildDataBody([]msim_data_pair{
			msim_new_data_int("ContactID", accountRow.Uid),
			msim_new_data_string("Headline", "schneider"),   //TODO
			msim_new_data_int("Position", 1),                //TODO
			msim_new_data_string("GroupName", "IM Friends"), //TODO
			msim_new_data_int("Visibility", 1),
			msim_new_data_string("ShowAvatar", "true"),
			msim_new_data_string("AvatarUrl", escapeString(accountRow.Avatar)),
			msim_new_data_int("LastLogin", 128177889600000000), //TODO
			msim_new_data_string("IMName", accountRow.Username),
			msim_new_data_string("NickName", accountRow.Screenname),
			msim_new_data_int("NameSelect", 0),
			msim_new_data_string("OfflineMsg", "im offline"),
			msim_new_data_int("SkyStatus", 0),
		})
	}
	res.Close()
	resp := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", body),
	})
	util.WriteTraffic(client.Connection, resp)
}

// persist 1;0;2 get_contact_information
func handleClientPacketGetContactInformation(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)

	parsedbody := strings.Split(findValueFromKey("body", packet), "=")

	util.Debug("get_contact_information")
	parse, _ := strconv.Atoi(parsedbody[1])

	accountRow := getUserDataById(parse)
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_int("ContactID", accountRow.Uid),
			msim_new_data_string("Headline", "schneider"),    //TODO
			msim_new_data_int("Position", 1),                 //TODO
			msim_new_data_string("!GroupName", "IM Friends"), //TODO
			msim_new_data_int("Visibility", 1),
			msim_new_data_string("!ShowAvatar", "true"),
			msim_new_data_string("!AvatarUrl", escapeString(accountRow.Avatar)),
			msim_new_data_int("!NameSelect", 0),
			msim_new_data_string("IMName", accountRow.Username),
			msim_new_data_string("!NickName", accountRow.Screenname),
		})),
	})
	util.WriteTraffic(client.Connection, res)
}

// Persist 1;1;4
func handleClientPacketUserLookupIMAboutMyself(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)

	parse := client.Account.Uid

	accountRow := getUserDataById(parse)
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_int("UserID", accountRow.Uid),
			msim_new_data_string("Sound", "true"),
			msim_new_data_int("!PrivacyMode", 0),
			msim_new_data_string("!ShowOnlyToList", "False"),
			msim_new_data_int("!OfflineMessageMode", 2),
			msim_new_data_string("Headline", "schneider"), //TODO
			msim_new_data_string("Avatarurl", escapeString(accountRow.Avatar)),
			msim_new_data_int("Alert", 1),
			msim_new_data_string("!ShowAvatar", "true"),
			msim_new_data_string("IMName", accountRow.Screenname),
			msim_new_data_int("!ClientVersion", 999),
			msim_new_data_string("!AllowBrowse", "true"),
			msim_new_data_string("IMLang", "English"),
			msim_new_data_int("LangID", 8192),
		})),
	})
	util.WriteTraffic(client.Connection, res)
}

// Persist 1;1;17
func handleClientPacketUserLookupIMByUid(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)

	parsedbody := strings.Split(findValueFromKey("body", packet), "=")
	parse, _ := strconv.Atoi(parsedbody[1])

	accountRow := getUserDataById(parse)
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_int("UserID", accountRow.Uid),
			msim_new_data_string("Sound", "true"),
			msim_new_data_int("!PrivacyMode", 0),             // TODO
			msim_new_data_string("!ShowOnlyToList", "False"), // TODO
			msim_new_data_int("!OfflineMessageMode", 2),      // TODO
			msim_new_data_string("Headline", "schneider"),    // TODO
			msim_new_data_string("Avatarurl", escapeString(accountRow.Avatar)),
			msim_new_data_int("Alert", 1),               //TODO
			msim_new_data_string("!ShowAvatar", "true"), // TODO
			msim_new_data_string("IMName", accountRow.Screenname),
			msim_new_data_int("!ClientVersion", 999),
			msim_new_data_string("!AllowBrowse", "true"), // TODO
			msim_new_data_string("IMLang", "English"),
			msim_new_data_int("LangID", 8192),
		})),
	})
	util.WriteTraffic(client.Connection, res)
}

// persist 1;2;6
// \persist\1\sesskey\7920\cmd\1\dsn\2\uid\1\lid\6\rid\8\body\\final\
func handleClientPacketGetGroups(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)

	util.Debug("get_contact_groups")
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_int("GroupID", 21672248),
			msim_new_data_string("GroupName", "IM Friends"),
			msim_new_data_int("Position", 1),
			msim_new_data_int("GroupFlag", 131073),
		})),
	})
	util.WriteTraffic(client.Connection, res)
}

// Persist 1;4;3, 1;4;5
func handleClientPacketUserLookupMySpaceByUid(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)
	parsedbody := strings.Split(findValueFromKey("body", packet), "=")

	parse, _ := strconv.Atoi(parsedbody[1])
	accountRow := getUserDataById(parse)
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_string("UserName", accountRow.Username),
			msim_new_data_int("UserID", accountRow.Uid),
			msim_new_data_string("ImageURL", escapeString(accountRow.Avatar)),
			msim_new_data_string("DisplayName", accountRow.Screenname),
			msim_new_data_string("BandName", accountRow.BandName),
			msim_new_data_string("SongName", accountRow.SongName),
			msim_new_data_string("Age", accountRow.Age),
			msim_new_data_string("Gender", accountRow.Gender),
			msim_new_data_string("Location", accountRow.Location),
			msim_new_data_int("!TotalFriends", 1), //TODO
		})),
	})
	util.WriteTraffic(client.Connection, res)
}

// Persist 1;5;7
func handleClientPacketUserLookupMySpaceByUsernameOrEmail(client *Msim_client, packet []byte) {
	cmd, _ := strconv.Atoi(findValueFromKey("cmd", packet))
	dsn := findValueFromKey("dsn", packet)
	lid := findValueFromKey("lid", packet)

	parsedbody := strings.Split(findValueFromKey("body", packet), "=")
	accountRow := getUserData(parsedbody[1])
	res := buildDataPacket([]msim_data_pair{
		msim_new_data_boolean("persistr", true),
		msim_new_data_int("uid", client.Account.Uid),
		msim_new_data_int("cmd", cmd^256),
		msim_new_data_string("dsn", dsn),
		msim_new_data_string("lid", lid),
		msim_new_data_string("rid", findValueFromKey("rid", packet)),
		msim_new_data_dictonary("body", buildDataBody([]msim_data_pair{
			msim_new_data_string(parsedbody[0], parsedbody[1]),
			msim_new_data_int("UserID", accountRow.Uid),
			msim_new_data_string("ImageURL", escapeString(accountRow.Avatar)),
			msim_new_data_string("DisplayName", accountRow.Screenname),
			msim_new_data_string("BandName", accountRow.BandName),
			msim_new_data_string("SongName", accountRow.SongName),
			msim_new_data_string("Age", accountRow.Age),
			msim_new_data_string("Gender", accountRow.Gender),
			msim_new_data_string("Location", accountRow.Location),
		})),
	})
	util.WriteTraffic(client.Connection, res)
}
