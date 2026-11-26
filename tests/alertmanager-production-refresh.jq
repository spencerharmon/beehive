# Read-only production probe: two GET snapshots, at least one refresh apart.
# No fixtures, POSTs, invented failures, or resolution of real alerts.
# jq -e -s --arg instance <registry-name> -f tests/alertmanager-production-refresh.jq

# jq's fromdateiso8601 accepts only whole-second UTC Z. Alertmanager may
# serialize a numeric RFC3339 offset; normalize the instant, not just its suffix.
# Retain fractions so short advances and expiration checks are not rounded away.
def timestamp:
  (capture("^(?<date>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?<fraction>\\.[0-9]+)?(?<zone>Z|(?<sign>[+-])(?<hours>[01][0-9]|2[0-3]):(?<minutes>[0-5][0-9]))$")
    // error("invalid RFC3339 timestamp")) as $t
  | (($t.date + "Z") | fromdateiso8601)
    + (("0" + ($t.fraction // "")) | tonumber)
    - (if $t.zone == "Z" then 0
       else ((($t.hours | tonumber) * 3600 + ($t.minutes | tonumber) * 60)
             * (if $t.sign == "+" then 1 else -1 end))
       end);
def identity: .labels | to_entries | sort_by(.key);
def valid:
  type == "array" and length > 0
  and all(.[];
    .labels.service == "beehive" and .labels.instance == $instance
    and .status.state == "active"
    and (.startsAt | timestamp) <= now
    and (.endsAt | timestamp) > now)
  and ([.[] | identity] | length == (unique | length));

if length != 2 or (all(.[]; valid) | not) then
  error("need two nonempty, unique, active production snapshots with unexpired endsAt")
else
  map(sort_by(identity)) as $s
  | if ($s[0] | map({labels, startsAt})) != ($s[1] | map({labels, startsAt})) then
      error("production identities/startsAt changed between snapshots; re-check current committed conditions")
    elif ([range(0; $s[0] | length) as $i
           | ($s[1][$i].endsAt | timestamp) > ($s[0][$i].endsAt | timestamp)] | all | not) then
      error("daemon did not advance every production endsAt; config/source alone is not activation")
    else
      [range(0; $s[0] | length) as $i | {
        labels: $s[1][$i].labels,
        startsAt: $s[1][$i].startsAt,
        firstEndsAt: $s[0][$i].endsAt,
        secondEndsAt: $s[1][$i].endsAt,
        advanceSeconds: (($s[1][$i].endsAt | timestamp) - ($s[0][$i].endsAt | timestamp))
      }]
    end
end
