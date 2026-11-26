# Read-only production probe: two GET snapshots, at least one refresh apart.
# No fixtures, POSTs, invented failures, or resolution of real alerts.
# jq -e -s --arg instance <registry-name> -f tests/alertmanager-production-refresh.jq

# Alertmanager renders RFC3339 with fractional seconds and the server's zone
# offset (e.g. 2026-11-26T04:32:48.501-06:00); fromdateiso8601 accepts only
# whole-second UTC "Z", so normalise explicitly and fail closed on anything else.
def timestamp:
  . as $raw
  | (capture("^(?<d>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\\.(?<f>[0-9]+))?(?<z>Z|(?<sign>[+-])(?<hh>[0-9]{2}):(?<mm>[0-9]{2}))$")
     // error("unparseable timestamp: \($raw)"))
  | ((.d + "Z") | fromdateiso8601)
    + (if .f then ("0." + .f | tonumber) else 0 end)
    - (if .z == "Z" then 0
       else (if .sign == "-" then -1 else 1 end) * ((.hh | tonumber) * 3600 + (.mm | tonumber) * 60)
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
        advanceSeconds: (((($s[1][$i].endsAt | timestamp) - ($s[0][$i].endsAt | timestamp)) * 1000 | round) / 1000)
      }]
    end
end
