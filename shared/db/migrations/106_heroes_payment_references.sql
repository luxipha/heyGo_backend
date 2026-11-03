-- Active references are unique; a corrected record remains in immutable history.
ALTER TABLE heroes_manual_payments DROP CONSTRAINT heroes_manual_payments_reference_key;
CREATE TABLE heroes_payment_references (
 reference TEXT PRIMARY KEY,
 payment_id UUID NOT NULL UNIQUE REFERENCES heroes_manual_payments(id)
);
INSERT INTO heroes_payment_references(reference,payment_id)
 SELECT p.reference,p.id FROM heroes_manual_payments p
 WHERE NOT EXISTS(SELECT 1 FROM heroes_payment_reversals r WHERE r.payment_id=p.id);
