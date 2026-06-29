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

