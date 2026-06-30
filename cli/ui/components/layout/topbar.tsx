export function Topbar({
  cloudURL,
  orgID,
  pat,
  onCloudURL,
  onOrgID,
  onPAT,
  onRefresh,
}: {
  cloudURL: string;
  orgID: string;
  pat: string;
  onCloudURL: (value: string) => void;
  onOrgID: (value: string) => void;
  onPAT: (value: string) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="topbar">
      <label>
        <span className="srOnly">Cloud URL</span>
        <input className="field" onChange={(event) => onCloudURL(event.target.value)} value={cloudURL} />
      </label>
      <label>
        <span className="srOnly">Org ID</span>
        <input className="field" onChange={(event) => onOrgID(event.target.value)} value={orgID} />
      </label>
      <label>
        <span className="srOnly">PAT</span>
        <input
          autoComplete="off"
          className="field"
          onChange={(event) => onPAT(event.target.value)}
          placeholder="PAT for secured Cloud"
          type="password"
          value={pat}
        />
      </label>
      <button onClick={onRefresh} type="button">
        Refresh
      </button>
    </div>
  );
}
