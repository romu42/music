//
// Johan Stenstam, johan.stenstam@internetstiftelsen.se
//

package music

import (
	"log"
)

const (
      AutoZones = `
SELECT name, zonetype, fsm, fsmsigner, fsmstatus
FROM zones WHERE fsmmode='auto' AND fsm != '' AND fsmstatus != 'stop'`
)

// PushZones: Try to move all "auto" zones forward through their respective processes until they
//            hit a stop.
//
// Note that we also need to add management for:
// (a) trying stopped zones, but less frequently, as they may have become unwedged
// (b) 

func (mdb *MusicDB) PushZones() error {
     var zones []string
     stmt, err := mdb.Prepare(AutoZones)
     if err != nil {
     	log.Fatalf("PushZones: Error from mdb.Prepare(%s): %v", AutoZones, err)
     }

     tx, err := mdb.Begin()
     if err != nil {
     	log.Fatalf("PushZones: Error from mdb.Begin(): %v", err)
     }

     	rows, err := stmt.Query()
	if err != nil {
		log.Printf("PushZones: Error from stmt query(%s): %v", AutoZones, err)
	}
	defer rows.Close()

	if CheckSQLError("PushZones", AutoZones, err, false) {
		return err
	} else {
	  var name, zonetype, fsm, fsmsigner, fsmstate string
	  for rows.Next() {
	      err := rows.Scan(&name, &zonetype, &fsm, &fsmsigner, &fsmstate)
	      if err != nil {
	      	 log.Fatalf("PushZones: Error from rows.Scan: %v", err)
	      }

	      zones = append(zones, name)

	  }
	}
	tx.Commit()
	
	log.Printf("PushZones: will push on these zones: %v", zones)
	for _, z := range zones {
	    mdb.PushZone(z)
	}
	return nil
}

func (mdb *MusicDB) PushZone(z string) error {
     dbzone, _ := mdb.GetZone(z)
     success, _, _ := mdb.ZoneStepFsm(dbzone, "")
     oldstate := dbzone.State
     if success {
     	dbzone, _ := mdb.GetZone(z)
     	log.Printf("PushZone: successfully transitioned zone '%s' from '%s' to '%s'",
			      z, oldstate, dbzone.State)
     } else {
       log.Printf("PushZone: failed to transition zone '%s' from state '%s'",
       			     z, oldstate)
     }
     return nil
}