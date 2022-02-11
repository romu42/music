package fsm

import (
	"fmt"
	"log"

	"github.com/miekg/dns"
        music "github.com/DNSSEC-Provisioning/music/common"
)

var FsmLeaveSyncNses = music.FSMTransition{
	Description: "First step when leaving, this transistion has no critera and will remove NSes that originated from the leaving signer (Action)",

	MermaidPreCondDesc:  "None",
	MermaidActionDesc:   "Remove NS records that only belong to the leaving signer",
	MermaidPostCondDesc: "Verify that NS records have been removed from zone",

	PreCondition:    LeaveSyncNsesPreCondition,
	Action:      	 LeaveSyncNsesAction,
	PostCondition:    func (z *music.Zone) bool { return true },
}

func LeaveSyncNsesPreCondition(z *music.Zone) bool {
	return true
}

func LeaveSyncNsesAction(z *music.Zone) bool {
	if z.ZoneType == "debug" {
	   log.Printf("LeaveSyncNsesAction: zone %s (DEBUG) is automatically ok", z.Name)
	   return true
	}

	sg := z.SignerGroup()
	if sg == nil {
	   log.Fatalf("Zone %s in process %s not attached to any signer group.", z.Name, z.FSM)
	}
	
	leavingSignerName := z.FSMSigner // Issue #34: Static leaving signer until metadata is in place
	if leavingSignerName == "" {
		log.Fatalf("Leaving signer name for zone %s unset.", z.Name)
	}

	// Need to get signer to remove records for it also, since it's not part of zone SignerMap anymore
	leavingSigner, err := z.MusicDB.GetSignerByName(leavingSignerName, false) // not apisafe
	if err != nil {
		z.SetStopReason(fmt.Sprintf("Unable to get leaving signer %s: %s", leavingSignerName, err))
		return false
	}

	log.Printf("%s: Removing NSes originating from leaving signer %s", z.Name, leavingSigner.Name)

	stmt, err := z.MusicDB.Prepare("SELECT ns FROM zone_nses WHERE zone = ? AND signer = ?")
	if err != nil {
		log.Printf("%s: Statement prepare failed: %s", z.Name, err)
		return false
	}

	rows, err := stmt.Query(z.Name, leavingSigner.Name)
	if err != nil {
		log.Printf("%s: Statement execute failed: %s", z.Name, err)
		return false
	}

	nsrem := []dns.RR{}

	var ns string
	for rows.Next() {
		if err = rows.Scan(&ns); err != nil {
			log.Printf("%s: Rows.Scan() failed: %s", z.Name, err)
			return false
		}

		rr := new(dns.NS)
		rr.Hdr = dns.RR_Header{Name: z.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 0}
		rr.Ns = ns
		nsrem = append(nsrem, rr)
	}

	for _, signer := range z.SGroup.SignerMap {
		updater := music.GetUpdater(signer.Method)
		if err := updater.Update(signer, z.Name, z.Name, nil, &[][]dns.RR{nsrem}); err != nil {
			z.SetStopReason(fmt.Sprintf("Unable to remove NSes from %s: %s", signer.Name, err))
			return false
		}
		log.Printf("%s: Removed NSes from %s successfully", z.Name, signer.Name)
	}

	updater := music.GetUpdater(leavingSigner.Method)
	if err := updater.Update(leavingSigner, z.Name, z.Name, nil, &[][]dns.RR{nsrem}); err != nil {
		z.SetStopReason(fmt.Sprintf("Unable to remove NSes from %s: %s", leavingSigner.Name, err))
		return false
	}
	log.Printf("%s: Removed NSes from %s successfully", z.Name, leavingSigner.Name)

	return true
}

