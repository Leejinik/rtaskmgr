export namespace host {
	
	export class Host {
	    id: string;
	    name: string;
	    addr: string;
	    port: number;
	    user: string;
	    password: string;
	    keyPath: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	    clusterId?: string;
	    clusterName?: string;
	    lizExpDays?: number;
	    rootExpDays?: number;
	    // Go type: time
	    pwCheckedAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new Host(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.addr = source["addr"];
	        this.port = source["port"];
	        this.user = source["user"];
	        this.password = source["password"];
	        this.keyPath = source["keyPath"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.clusterId = source["clusterId"];
	        this.clusterName = source["clusterName"];
	        this.lizExpDays = source["lizExpDays"];
	        this.rootExpDays = source["rootExpDays"];
	        this.pwCheckedAt = this.convertValues(source["pwCheckedAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ClusterConnectResult {
	    hostId: string;
	    caps: monitor.Capabilities;
	    err: string;
	
	    static createFrom(source: any = {}) {
	        return new ClusterConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.caps = this.convertValues(source["caps"], monitor.Capabilities);
	        this.err = source["err"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogHostInfo {
	    id: string;
	    name: string;
	    frames: number;
	    startT: number;
	    endT: number;
	
	    static createFrom(source: any = {}) {
	        return new LogHostInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.frames = source["frames"];
	        this.startT = source["startT"];
	        this.endT = source["endT"];
	    }
	}
	export class LogMeta {
	    path: string;
	    hosts: LogHostInfo[];
	    stride: number;
	
	    static createFrom(source: any = {}) {
	        return new LogMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.hosts = this.convertValues(source["hosts"], LogHostInfo);
	        this.stride = source["stride"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace monitor {
	
	export class Capabilities {
	    uid: number;
	    os: string;
	    rhel: string;
	    nethogs: boolean;
	    pidstat: boolean;
	    sudo: boolean;
	    stageDir: string;
	
	    static createFrom(source: any = {}) {
	        return new Capabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uid = source["uid"];
	        this.os = source["os"];
	        this.rhel = source["rhel"];
	        this.nethogs = source["nethogs"];
	        this.pidstat = source["pidstat"];
	        this.sudo = source["sudo"];
	        this.stageDir = source["stageDir"];
	    }
	}
	export class DiskStat {
	    mount: string;
	    dev: string;
	    fsType: string;
	    total: number;
	    free: number;
	    used: number;
	    rBps: number;
	    wBps: number;
	    busy: number;
	    kind: string;
	
	    static createFrom(source: any = {}) {
	        return new DiskStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mount = source["mount"];
	        this.dev = source["dev"];
	        this.fsType = source["fsType"];
	        this.total = source["total"];
	        this.free = source["free"];
	        this.used = source["used"];
	        this.rBps = source["rBps"];
	        this.wBps = source["wBps"];
	        this.busy = source["busy"];
	        this.kind = source["kind"];
	    }
	}
	export class Proc {
	    pid: number;
	    ppid: number;
	    name: string;
	    user: string;
	    service: string;
	    state: string;
	    cpu: number;
	    memPct: number;
	    rssKiB: number;
	    diskR: number;
	    diskW: number;
	    net: number;
	    threads: number;
	    start: number;
	
	    static createFrom(source: any = {}) {
	        return new Proc(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.ppid = source["ppid"];
	        this.name = source["name"];
	        this.user = source["user"];
	        this.service = source["service"];
	        this.state = source["state"];
	        this.cpu = source["cpu"];
	        this.memPct = source["memPct"];
	        this.rssKiB = source["rssKiB"];
	        this.diskR = source["diskR"];
	        this.diskW = source["diskW"];
	        this.net = source["net"];
	        this.threads = source["threads"];
	        this.start = source["start"];
	    }
	}
	export class NetStat {
	    name: string;
	    rxBps: number;
	    txBps: number;
	    speed: number;
	
	    static createFrom(source: any = {}) {
	        return new NetStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.rxBps = source["rxBps"];
	        this.txBps = source["txBps"];
	        this.speed = source["speed"];
	    }
	}
	export class Frame {
	    hostId: string;
	    t: number;
	    ncpu: number;
	    memTotal: number;
	    memUsed: number;
	    cpu: number;
	    mem: number;
	    swapTotal: number;
	    swapUsed: number;
	    netRx: number;
	    netTx: number;
	    netSpeed: number;
	    nets: NetStat[];
	    disks: DiskStat[];
	    procs: Proc[];
	
	    static createFrom(source: any = {}) {
	        return new Frame(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.t = source["t"];
	        this.ncpu = source["ncpu"];
	        this.memTotal = source["memTotal"];
	        this.memUsed = source["memUsed"];
	        this.cpu = source["cpu"];
	        this.mem = source["mem"];
	        this.swapTotal = source["swapTotal"];
	        this.swapUsed = source["swapUsed"];
	        this.netRx = source["netRx"];
	        this.netTx = source["netTx"];
	        this.netSpeed = source["netSpeed"];
	        this.nets = this.convertValues(source["nets"], NetStat);
	        this.disks = this.convertValues(source["disks"], DiskStat);
	        this.procs = this.convertValues(source["procs"], Proc);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class PwStatus {
	    hasLiz: boolean;
	    hasRoot: boolean;
	    lizExpDays: number;
	    rootExpDays: number;
	    todayDays: number;
	
	    static createFrom(source: any = {}) {
	        return new PwStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasLiz = source["hasLiz"];
	        this.hasRoot = source["hasRoot"];
	        this.lizExpDays = source["lizExpDays"];
	        this.rootExpDays = source["rootExpDays"];
	        this.todayDays = source["todayDays"];
	    }
	}
	export class RecTarget {
	    path: string;
	    mount: string;
	    totalBytes: number;
	    freeBytes: number;
	    writable: boolean;
	    needsSudo: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RecTarget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.mount = source["mount"];
	        this.totalBytes = source["totalBytes"];
	        this.freeBytes = source["freeBytes"];
	        this.writable = source["writable"];
	        this.needsSudo = source["needsSudo"];
	    }
	}
	export class RecEstimate {
	    targets: RecTarget[];
	    probeSec: number;
	    probeBytes: number;
	    frames: number;
	    bytesPerHour: number;
	    bytesPerDay: number;
	
	    static createFrom(source: any = {}) {
	        return new RecEstimate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.targets = this.convertValues(source["targets"], RecTarget);
	        this.probeSec = source["probeSec"];
	        this.probeBytes = source["probeBytes"];
	        this.frames = source["frames"];
	        this.bytesPerHour = source["bytesPerHour"];
	        this.bytesPerDay = source["bytesPerDay"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RecMeta {
	    id: string;
	    hostId: string;
	    hostName: string;
	    file: string;
	    startT: number;
	    plannedEndT: number;
	    durationSec: number;
	    intervalSec: number;
	    status: string;
	    sizeBytes: number;
	    lastT: number;
	    doneReason: string;
	
	    static createFrom(source: any = {}) {
	        return new RecMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.file = source["file"];
	        this.startT = source["startT"];
	        this.plannedEndT = source["plannedEndT"];
	        this.durationSec = source["durationSec"];
	        this.intervalSec = source["intervalSec"];
	        this.status = source["status"];
	        this.sizeBytes = source["sizeBytes"];
	        this.lastT = source["lastT"];
	        this.doneReason = source["doneReason"];
	    }
	}

}

export namespace pwledger {
	
	export class Config {
	    tempPassword: string;
	    expiryWarnDays: number;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tempPassword = source["tempPassword"];
	        this.expiryWarnDays = source["expiryWarnDays"];
	    }
	}
	export class Entry {
	    id: string;
	    hostId: string;
	    hostName: string;
	    addr: string;
	    account: string;
	    op: string;
	    step: string;
	    password: string;
	    status: string;
	    err?: string;
	    // Go type: time
	    at: any;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.addr = source["addr"];
	        this.account = source["account"];
	        this.op = source["op"];
	        this.step = source["step"];
	        this.password = source["password"];
	        this.status = source["status"];
	        this.err = source["err"];
	        this.at = this.convertValues(source["at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace record {
	
	export class Point {
	    t: number;
	    cpu: number;
	    memPct: number;
	    rssKiB: number;
	    diskR: number;
	    diskW: number;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.t = source["t"];
	        this.cpu = source["cpu"];
	        this.memPct = source["memPct"];
	        this.rssKiB = source["rssKiB"];
	        this.diskR = source["diskR"];
	        this.diskW = source["diskW"];
	    }
	}

}

export namespace updater {
	
	export class UpdateInfo {
	    available: boolean;
	    currentVersion: string;
	    latestVersion: string;
	    releaseNotes: string;
	    downloadUrl: string;
	    publishedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.currentVersion = source["currentVersion"];
	        this.latestVersion = source["latestVersion"];
	        this.releaseNotes = source["releaseNotes"];
	        this.downloadUrl = source["downloadUrl"];
	        this.publishedAt = source["publishedAt"];
	    }
	}
	export class AutoUpdateResult {
	    applying: boolean;
	    blocked: boolean;
	    info: UpdateInfo;
	
	    static createFrom(source: any = {}) {
	        return new AutoUpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.applying = source["applying"];
	        this.blocked = source["blocked"];
	        this.info = this.convertValues(source["info"], UpdateInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PendingNotes {
	    version: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingNotes(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.notes = source["notes"];
	    }
	}

}

