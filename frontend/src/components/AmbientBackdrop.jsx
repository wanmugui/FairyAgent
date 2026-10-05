import React from 'react';

const LOGOS = Array.from({ length: 8 });

function LogoUnit() {
  return (
    <span className="zzz-logo-unit">
      <b>FAIRY</b>
    </span>
  );
}

function LogoRun({ prefix }) {
  return (
    <span className="zzz-logo-run">
      {LOGOS.map((_, index) => <LogoUnit key={prefix + '-' + index} />)}
    </span>
  );
}

export default function AmbientBackdrop() {
  return (
    <div className="zzz-ambient-backdrop" aria-hidden="true">
      <div className="zzz-logo-track zzz-logo-track-a">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="a1" />
          <LogoRun prefix="a2" />
        </div>
      </div>
      <div className="zzz-logo-track zzz-logo-track-b">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="b1" />
          <LogoRun prefix="b2" />
        </div>
      </div>
      <div className="zzz-logo-track zzz-logo-track-c">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="c1" />
          <LogoRun prefix="c2" />
        </div>
      </div>
      <div className="zzz-logo-track zzz-logo-track-d">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="d1" />
          <LogoRun prefix="d2" />
        </div>
      </div>
      <div className="zzz-logo-track zzz-logo-track-e">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="e1" />
          <LogoRun prefix="e2" />
        </div>
      </div>
      <div className="zzz-logo-track zzz-logo-track-f">
        <div className="zzz-logo-scroll">
          <LogoRun prefix="f1" />
          <LogoRun prefix="f2" />
        </div>
      </div>
    </div>
  );
}
