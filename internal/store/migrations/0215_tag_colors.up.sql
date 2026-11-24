-- Six more tag colours. Widening the check is additive: an old task never writes the new values, and it shows a colour it
-- does not know as a neutral chip.
alter table tags drop constraint tags_color_check;
alter table tags add constraint tags_color_check check (
  color in ('neutral','red','orange','amber','brown','lime','green','teal','blue','indigo','violet','pink')
);
