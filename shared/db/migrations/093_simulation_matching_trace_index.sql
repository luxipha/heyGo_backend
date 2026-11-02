CREATE INDEX simulation_matching_trace ON simulation_events(simulation_run_id,(data->>'tripId'),id DESC) WHERE kind='matching.candidates';
