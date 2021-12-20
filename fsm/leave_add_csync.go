package fsm

import (
	// "fmt"
	"log"

	"github.com/miekg/dns"
        music "github.com/DNSSEC-Provisioning/music/common"
)

var FsmLeaveAddCsync = music.FSMTransition{
	Description: "Once all NS are correct in all signers (criteria), build CSYNC record and push to all signers (action)",

	MermaidPreCondDesc:  "Wait for all NS RRsets to be in sync in all signers",
	MermaidActionDesc:   "Create and publish CSYNC record in all signers",
	MermaidPostCondDesc: "Verify that the CSYNC record has been removed everywhere",
	
	PreCondition:   LeaveAddCsyncPreCondition,
	Action:      	LeaveAddCsyncAction,
	PostCondition:	func (z *music.Zone) bool { return true },
}

func LeaveAddCsyncPreCondition(z *music.Zone) bool {
	leavingSignerName := "signer2.catch22.se." // Issue #34: Static leaving signer until metadata is in place

	// Need to get signer to remove records for it also, since it's not part of zone SignerMap anymore
	leavingSigner, err := z.MusicDB.GetSignerByName(leavingSignerName, false) // not apisafe
	if err != nil {
		log.Printf("%s: Unable to get leaving signer %s: %s", z.Name, leavingSignerName, err)
		return false
	}

	nses := make(map[string]bool)

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

	var ns string
	for rows.Next() {
		if err = rows.Scan(&ns); err != nil {
			log.Printf("%s: Rows.Scan() failed: %s", z.Name, err)
			return false
		}

		nses[ns] = true
	}

	log.Printf("%s: Verifying that leaving signer %s NSes has been removed from all signers", z.Name, leavingSigner.Name)

	for _, s := range z.SGroup.SignerMap {
		m := new(dns.Msg)
		m.SetQuestion(z.Name, dns.TypeNS)
		c := new(dns.Client)
		r, _, err := c.Exchange(m, s.Address+":53") // TODO: add DnsAddress or solve this in a better way
		if err != nil {
			log.Printf("%s: Unable to fetch NSes from %s: %s", z.Name, s.Name, err)
			return false
		}

		for _, a := range r.Answer {
			ns, ok := a.(*dns.NS)
			if !ok {
				continue
			}

			if _, ok := nses[ns.Ns]; ok {
				log.Printf("%s: NS %s still exists in signer %s", z.Name, ns.Ns, s.Name)
				return false
			}
		}
	}

	m := new(dns.Msg)
	m.SetQuestion(z.Name, dns.TypeNS)
	c := new(dns.Client)
	r, _, err := c.Exchange(m, leavingSigner.Address+":53") // TODO: add DnsAddress or solve this in a better way
	if err != nil {
		log.Printf("%s: Unable to fetch NSes from %s: %s", z.Name, leavingSigner.Name, err)
		return false
	}

	for _, a := range r.Answer {
		ns, ok := a.(*dns.NS)
		if !ok {
			continue
		}

		if _, ok := nses[ns.Ns]; ok {
			log.Printf("%s: NS %s still exists in signer %s", z.Name, ns.Ns, leavingSigner.Name)
			return false
		}
	}

	log.Printf("%s: All NSes of leaving signer has been removed", z.Name)
	return true
}

// Semantics:
// 1. Lookup zone signergroup (can only be one)
// 2. Lookup all signers in signergroup.PendingRemoval
// 3. For each signer in that list (should really only be one) go through the steps below.
// 4. Celebrate Christmas

func LeaveAddCsyncAction(z *music.Zone) bool {
	leavingSignerName := "signer2.catch22.se." // Issue #34: Static leaving signer until metadata is in place

	// Need to get signer to remove records for it also, since it's not part of zone SignerMap anymore
	leavingSigner, err := z.MusicDB.GetSignerByName(leavingSignerName, false) // not apisafe
	if err != nil {
		log.Printf("%s: Unable to get leaving signer %s: %s", z.Name, leavingSignerName, err)
		return false
	}

	// TODO: configurable TTL for created CSYNC records
	ttl := 300

	log.Printf("%s: Creating CSYNC record sets", z.Name)

	for _, signer := range z.SGroup.SignerMap {
		m := new(dns.Msg)
		m.SetQuestion(z.Name, dns.TypeSOA)
		c := new(dns.Client)
		r, _, err := c.Exchange(m, signer.Address+":53") // TODO: add DnsAddress or solve this in a better way
		if err != nil {
			log.Printf("%s: Unable to fetch SOA from %s: %s", z.Name, signer.Name, err)
			return false
		}

		for _, a := range r.Answer {
			soa, ok := a.(*dns.SOA)
			if !ok {
				continue
			}

			csync := new(dns.CSYNC)
			csync.Hdr = dns.RR_Header{Name: z.Name, Rrtype: dns.TypeCSYNC, Class: dns.ClassINET, Ttl: uint32(ttl)}
			csync.Serial = soa.Serial
			csync.Flags = 3
			csync.TypeBitMap = []uint16{dns.TypeA, dns.TypeNS, dns.TypeAAAA}

			updater := music.GetUpdater(signer.Method)
			if err := updater.Update(signer, z.Name, z.Name,
				&[][]dns.RR{[]dns.RR{csync}}, nil); err != nil {
				log.Printf("%s: Unable to update %s with CSYNC record sets: %s", z.Name, signer.Name, err)
				return false
			}
			log.Printf("%s: Update %s successfully with CSYNC record sets", z.Name, signer.Name)
		}
	}

	m := new(dns.Msg)
	m.SetQuestion(z.Name, dns.TypeSOA)
	c := new(dns.Client)
	r, _, err := c.Exchange(m, leavingSigner.Address+":53") // TODO: add DnsAddress or solve this in a better way
	if err != nil {
		log.Printf("%s: Unable to fetch SOA from %s: %s", z.Name, leavingSigner.Name, err)
		return false
	}

	for _, a := range r.Answer {
		soa, ok := a.(*dns.SOA)
		if !ok {
			continue
		}

		csync := new(dns.CSYNC)
		csync.Hdr = dns.RR_Header{Name: z.Name, Rrtype: dns.TypeCSYNC, Class: dns.ClassINET, Ttl: uint32(ttl)}
		csync.Serial = soa.Serial
		csync.Flags = 3
		csync.TypeBitMap = []uint16{dns.TypeA, dns.TypeNS, dns.TypeAAAA}

		updater := music.GetUpdater(leavingSigner.Method)
		if err := updater.Update(leavingSigner, z.Name, z.Name,
			&[][]dns.RR{[]dns.RR{csync}}, nil); err != nil {
			log.Printf("%s: Unable to update %s with CSYNC record sets: %s",
				z.Name, leavingSigner.Name, err)
			return false
		}
		log.Printf("%s: Update %s successfully with CSYNC record sets", z.Name, leavingSigner.Name)
	}

	return true
}

