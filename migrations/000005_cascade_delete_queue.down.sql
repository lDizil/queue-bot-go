ALTER TABLE queue_entries 
DROP CONSTRAINT queue_entries_schedule_id_fkey;

ALTER TABLE queue_entries 
ADD CONSTRAINT queue_entries_schedule_id_fkey 
FOREIGN KEY (schedule_id) REFERENCES schedules(id);