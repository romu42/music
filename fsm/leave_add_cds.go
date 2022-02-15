package fsm

import (
	"fmt"
	"log"

	music "github.com/DNSSEC-Provisioning/music/common"
	"github.com/miekg/dns"
)

var FsmLeaveAddCDS = music.FSMTransition{
	Description: "Once all DNSKEYs are correct in all signers (criteria), build CDS/CDNSKEYs RRset and push to all signers (action)",

	MermaidPreCondDesc:  "TEXT",
	MermaidActionDesc:   "TEXT",
	MermaidPostCondDesc: "TEXT",

	PreCondition:  LeaveAddCDSPreCondition,
	Action:        LeaveAddCDSAction,
	PostCondition: func(z *music.Zone) bool { return true },
}

func LeaveAddCDSPreCondition(z *music.Zone) bool {
	if z.ZoneType == "debug" {
		log.Printf("LeaveAddCdsPreCondition: zone %s (DEBUG) is automatically ok", z.Name)
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

	log.Printf("%s: Verifying that leaving signer %s DNSKEYs has been removed from all signers",
		z.Name, leavingSigner.Name)

	stmt, err := z.MusicDB.Prepare("SELECT dnskey FROM zone_dnskeys WHERE zone = ? AND signer = ?")
	if err != nil {
		log.Printf("%s: Statement prepare failed: %s", z.Name, err)
		return false
	}

	rows, err := stmt.Query(z.Name, leavingSigner.Name)
	if err != nil {
		log.Printf("%s: Statement execute failed: %s", z.Name, err)
		return false
	}

	dnskeys := make(map[string]bool)

	var dnskey string
	for rows.Next() {
		if err = rows.Scan(&dnskey); err != nil {
			log.Printf("%s: Rows.Scan() failed: %s", z.Name, err)
			return false
		}

		dnskeys[dnskey] = true
	}

	for _, s := range z.SGroup.SignerMap {
		// the leaving signer is still in the SignerMap even though the logic in this file seems to think it should not be.
		// https://github.com/DNSSEC-Provisioning/music/issues/130
		// common/signerops.go seems to think that it should be. We need to decided what we really want here. /rog
		if s.Name == leavingSignerName {
			log.Printf("the leaving signer is still in the SignerMap, not sure which way the bug is but this is a work around for now.")
			continue
		}
		m := new(dns.Msg)
		m.SetQuestion(z.Name, dns.TypeDNSKEY)
		c := new(dns.Client)
		r, _, err := c.Exchange(m, s.Address+":"+s.Port)
		if err != nil {
			z.SetStopReason(fmt.Sprintf("Unable to fetch DNSKEYs from %s: %s", s.Name, err))
			return false
		}

		for _, a := range r.Answer {
			dnskey, ok := a.(*dns.DNSKEY)
			if !ok {
				continue
			}

			if _, ok := dnskeys[fmt.Sprintf("%d-%d-%s", dnskey.Protocol, dnskey.Algorithm, dnskey.PublicKey)]; ok {
				z.SetStopReason(fmt.Sprintf("DNSKEY %s still exists in signer %s",
					dnskey.PublicKey, s.Name))
				return false
			}
		}
	}

	return true
}

func LeaveAddCDSAction(z *music.Zone) bool {
	log.Printf("%s: Creating CDS/CDNSKEY record sets", z.Name)

	if z.ZoneType == "debug" {
		log.Printf("LeaveAddCdsAction: zone %s (DEBUG) is automatically ok", z.Name)
		return true
	}

	cdses := []dns.RR{}
	cdnskeys := []dns.RR{}

	// https://github.com/DNSSEC-Provisioning/music/issues/130 / rog
	leavingSignerName := z.FSMSigner // Issue #34: Static leaving signer until metadata is in place
	if leavingSignerName == "" {
		log.Fatalf("Leaving signer name for zone %s unset.", z.Name)
	}

	for _, s := range z.SGroup.SignerMap {
		if s.Name == leavingSignerName {
			log.Printf("issue 130: the leaving signer is still in the SignerMap, not sure which way the bug is but this is a work around for now.")
			continue
		}
		m := new(dns.Msg)
		m.SetQuestion(z.Name, dns.TypeDNSKEY)

		c := new(dns.Client)
		r, _, err := c.Exchange(m, s.Address+":"+s.Port)

		if err != nil {
			z.SetStopReason(fmt.Sprintf("Unable to fetch DNSKEYs from %s: %s", s.Name, err))
			return false
		}

		for _, a := range r.Answer {
			dnskey, ok := a.(*dns.DNSKEY)
			if !ok {
				continue
			}

			if f := dnskey.Flags & 0x101; f == 257 {
				cdses = append(cdses, dnskey.ToDS(dns.SHA256).ToCDS())
				cdnskeys = append(cdnskeys, dnskey.ToCDNSKEY())
			}
		}
	}

	// Create CDS/CDNSKEY records sets
	for _, signer := range z.SGroup.SignerMap {
		if signer.Name == leavingSignerName {
			log.Printf("issue 130: the leaving signer is still in the SignerMap, not sure which way the bug is but this is a work around for now.")
			continue
		}
		updater := music.GetUpdater(signer.Method)
		if err := updater.Update(signer, z.Name, z.Name,
			&[][]dns.RR{cdses, cdnskeys}, nil); err != nil {
			z.SetStopReason(fmt.Sprintf("Unable to update %s with CDS/CDNSKEY record sets: %s",
				signer.Name, err))
			return false
		}
		log.Printf("%s: Update %s successfully with CDS/CDNSKEY record sets", z.Name, signer.Name)
	}

	return true
}
