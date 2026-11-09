-- Only reserved offer slots are released; this never credits/debits a wallet.
CREATE FUNCTION release_cancelled_offers() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 UPDATE rider_offer_uses SET status='released' WHERE trip_id=NEW.id AND status='reserved';
 RETURN NEW; END $$;
CREATE TRIGGER cancelled_offer_release AFTER UPDATE OF status ON trips FOR EACH ROW WHEN(NEW.status='cancelled') EXECUTE FUNCTION release_cancelled_offers();
