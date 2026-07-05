export function Topbar({
  orgID,
  onOrgID,
  onRefresh,
}: {
  orgID: string;
  onOrgID: (value: string) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="topbar">
      <label>
        <span className="srOnly">Org ID</span>
        <input className="field" onChange={(event) => onOrgID(event.target.value)} value={orgID} />
      </label>
      <button onClick={onRefresh} type="button">
        Refresh
      </button>
    </div>
  );
}
