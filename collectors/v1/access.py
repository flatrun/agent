import json
import hashlib
import re
from datetime import datetime
from pathlib import Path


def timestamp(value):
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return result if result.tzinfo is not None else None
    except (TypeError, ValueError, AttributeError):
        return None


def evaluate(policy, event):
    unknown = {"decision": "unknown", "classification": "unknown"}
    if not isinstance(policy, dict) or not isinstance(event, dict):
        return unknown
    if policy.get("version") != "1" or not policy.get("revision") or event.get("policy_revision") != policy.get("revision"):
        return unknown
    actions = policy.get("actions")
    if not isinstance(actions, dict) or not isinstance(event.get("action"), str):
        return unknown
    action = actions.get(event["action"])
    if not isinstance(action, dict) or action.get("resource_type") != event.get("resource_type"):
        return unknown
    if event.get("integrity") != "authenticated_report":
        return unknown
    if action.get("public") is True:
        decision = "allow"
    else:
        authenticated = event.get("authenticated")
        if action.get("require_authentication") and authenticated is False:
            decision = "deny"
        elif action.get("require_authentication") and authenticated is not True:
            return unknown
        else:
            at = timestamp(event.get("at"))
            start = timestamp(event.get("facts_valid_from"))
            end = timestamp(event.get("facts_valid_until"))
            if action.get("allow") and (at is None or start is None or end is None or not start <= at < end):
                return unknown
            facts = event.get("facts", {})
            if not isinstance(facts, dict) or not isinstance(action.get("allow", []), list):
                return unknown
            results = []
            for clause in action.get("allow", []):
                if not isinstance(clause, dict):
                    results.append(None)
                    continue
                kind = clause.get("kind")
                if kind in ("tenant_member", "resource_owner", "explicit_share"):
                    result = facts.get(kind)
                    results.append(result if type(result) is bool else None)
                elif kind == "role" and clause.get("scope") in ("tenant", "global"):
                    roles = facts.get("roles")
                    if not isinstance(roles, list):
                        results.append(None)
                    else:
                        matches = []
                        for role in roles:
                            if not isinstance(role, dict):
                                matches.append(None)
                            elif role.get("role") not in (None, clause.get("role")) or role.get("scope") not in (None, clause["scope"]):
                                matches.append(False)
                            elif role.get("role") is None or role.get("scope") is None:
                                matches.append(None)
                            elif clause["scope"] == "global":
                                matches.append(True)
                            elif event.get("tenant_id") is None or role.get("tenant_id") is None:
                                matches.append(None)
                            else:
                                matches.append(role["tenant_id"] == event["tenant_id"])
                        results.append(True if True in matches else None if None in matches else False)
                else:
                    results.append(None)
            decision = "allow" if True in results else "unknown" if None in results else "deny"
    effect = event.get("protected_effect")
    candidate = decision == "deny" and effect == "demonstrated_success"
    classification = "unauthorized_access_candidate" if candidate else (
        "not_candidate" if decision == "allow" or effect == "demonstrated_failure" else "unknown"
    )
    return {"decision": decision, "classification": classification}


def main():
    inputs = json.loads(Path("/input/metadata.json").read_text())
    owner_policy = inputs["access_policy"]
    policy = dict(owner_policy["policy"])
    policy["revision"] = owner_policy["profile_revision"]
    events = inputs["audit_events"]["events"]
    if not isinstance(events, list) or len(events) > 96:
        raise ValueError("Access pages require at most 96 events")
    items = []
    for event in events:
        result = evaluate(policy, event)
        digest = hashlib.sha256(json.dumps(event, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        valid_identity = isinstance(event, dict) and all(isinstance(event.get(key), str) and re.fullmatch(r"[A-Za-z0-9_.:/-]{1,128}", event[key]) for key in ("event_id", "source"))
        if not valid_identity:
            result = {"decision": "unknown", "classification": "unknown"}
        result["event_id"] = event["event_id"] if valid_identity else digest
        result["source"] = event["source"] if valid_identity else "unknown"
        result["event_digest"] = digest
        items.append(result)
    candidates = sum(item["classification"] == "unauthorized_access_candidate" for item in items)
    print(json.dumps({
        "api_version": "flatrun.collector-result/v1", "process_status": "completed",
        "items": [{"candidate_count": candidates}] + items, "omissions": [], "boundaries": [],
        "limitations": ["Authenticated transport does not prove application-reported facts are truthful"],
        "coverage": {"declared_observation": "complete", "manifest_processing": "complete", "window_coverage": "unknown"},
    }))


if __name__ == "__main__":
    main()
