CREATE FUNCTION protect_pricing_zone_rule() RETURNS TRIGGER AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  PERFORM id FROM pricing_versions WHERE id=OLD.pricing_id FOR SHARE;
  IF EXISTS(SELECT 1 FROM pricing_versions WHERE id=OLD.pricing_id AND status='approved') THEN RAISE EXCEPTION 'published zone rules are immutable'; END IF;
 END IF;
 IF TG_OP<>'DELETE' THEN
  PERFORM id FROM pricing_versions WHERE id=NEW.pricing_id FOR SHARE;
  IF EXISTS(SELECT 1 FROM pricing_versions WHERE id=NEW.pricing_id AND status='approved') THEN RAISE EXCEPTION 'published zone rules are immutable'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER pricing_zone_rules_protect BEFORE INSERT OR UPDATE OR DELETE ON pricing_zone_rules FOR EACH ROW EXECUTE FUNCTION protect_pricing_zone_rule();
